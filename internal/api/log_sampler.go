package api

import (
	"log/slog"
	"sync"
	"time"
)

// Tope de las líneas que puede provocar una entrada sin autenticar (firma o clave mala, límite
// superado, y la línea «request» de toda respuesta de error): por mensaje, authLogBurst líneas por
// authLogWindow, más un resumen al cerrar la ventana. Alcanza para diagnosticar una firma mal
// configurada y deja el volumen del log de una inundación anónima acotado, no proporcional a ella.
const (
	authLogBurst  = 10
	authLogWindow = time.Minute
)

// logSampler deja como mucho burst líneas de cada mensaje por ventana. Las que sobran se cuentan: al
// cerrar la ventana sale un resumen con el total en "suppressed" (también si después no llega nada
// más). Se keyea por el MENSAJE, que es un literal del código (conjunto fijo): nada que mande el
// cliente hace crecer el mapa. Un nil registra todo.
type logSampler struct {
	burst  int
	window time.Duration
	now    func() time.Time
	after  func(time.Duration, func()) // programa el resumen (time.AfterFunc; las pruebas lo cambian)

	mu      sync.Mutex
	windows map[string]*sampleWindow
}

type sampleWindow struct {
	start      time.Time
	emitted    int
	suppressed int
}

func newAuthLogSampler() *logSampler {
	return &logSampler{
		burst: authLogBurst, window: authLogWindow, now: time.Now,
		after:   func(d time.Duration, f func()) { time.AfterFunc(d, f) },
		windows: map[string]*sampleWindow{},
	}
}

// Warn es slog.Warn con el tope del muestreador.
func (s *logSampler) Warn(msg string, args ...any) { s.log(slog.LevelWarn, msg, args) }

// Info es slog.Info con el tope del muestreador.
func (s *logSampler) Info(msg string, args ...any) { s.log(slog.LevelInfo, msg, args) }

func (s *logSampler) log(level slog.Level, msg string, args []any) {
	if s == nil {
		emit(level, msg, args)
		return
	}
	s.mu.Lock()
	now := s.now()
	win := s.windows[msg]
	if win == nil || now.Sub(win.start) >= s.window {
		if win != nil && win.suppressed > 0 { // el resumen de esa ventana aún no salió
			args = append(args, "suppressed", win.suppressed)
		}
		win = &sampleWindow{start: now}
		s.windows[msg] = win
	}
	if win.emitted >= s.burst {
		win.suppressed++
		if win.suppressed == 1 {
			s.after(win.start.Add(s.window).Sub(now), func() { s.summarize(level, msg, win) })
		}
		s.mu.Unlock()
		return
	}
	win.emitted++
	s.mu.Unlock()
	emit(level, msg, args)
}

// summarize registra cuántas líneas se callaron en win, si sigue siendo la ventana vigente del mensaje
// (si ya empezó otra, su primera línea se llevó el total).
func (s *logSampler) summarize(level slog.Level, msg string, win *sampleWindow) {
	s.mu.Lock()
	n := win.suppressed
	if s.windows[msg] != win || n == 0 {
		s.mu.Unlock()
		return
	}
	win.suppressed = 0
	s.mu.Unlock()
	emit(level, msg, []any{"suppressed", n})
}

//nolint:gosec // G706: msg es un literal del código; los atributos son los que ya registraba cada llamador.
func emit(level slog.Level, msg string, args []any) {
	if level >= slog.LevelWarn {
		slog.Warn(msg, args...)
		return
	}
	slog.Info(msg, args...)
}
