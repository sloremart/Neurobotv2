package api

import (
	"log/slog"
	"sync"
	"time"
)

// Tope de las líneas WARN que puede provocar una entrada sin autenticar (firma o clave mala, límite
// superado): por mensaje, authLogBurst líneas por authLogWindow. Alcanza para diagnosticar una firma
// mal configurada y deja una inundación anónima en ~10 líneas por minuto en vez de una por petición.
const (
	authLogBurst  = 10
	authLogWindow = time.Minute
)

// logSampler deja como mucho burst líneas de cada mensaje por ventana; las que sobran se cuentan y la
// primera línea de la ventana siguiente lleva el total en "suppressed". Se keyea por el MENSAJE, que
// es un literal del código (conjunto fijo): nada que mande el cliente hace crecer el mapa. Un nil
// registra todo.
type logSampler struct {
	burst  int
	window time.Duration
	now    func() time.Time

	mu      sync.Mutex
	windows map[string]*sampleWindow
}

type sampleWindow struct {
	start      time.Time
	emitted    int
	suppressed int
}

func newAuthLogSampler() *logSampler {
	return &logSampler{burst: authLogBurst, window: authLogWindow, now: time.Now, windows: map[string]*sampleWindow{}}
}

// Warn es slog.Warn con el tope del muestreador.
func (s *logSampler) Warn(msg string, args ...any) {
	if s == nil {
		//nolint:gosec // G706: msg es un literal del código; los atributos son los que ya registraba cada llamador.
		slog.Warn(msg, args...)
		return
	}
	s.mu.Lock()
	now := s.now()
	win := s.windows[msg]
	if win == nil || now.Sub(win.start) >= s.window {
		if win != nil && win.suppressed > 0 {
			args = append(args, "suppressed", win.suppressed)
		}
		win = &sampleWindow{start: now}
		s.windows[msg] = win
	}
	if win.emitted >= s.burst {
		win.suppressed++
		s.mu.Unlock()
		return
	}
	win.emitted++
	s.mu.Unlock()
	//nolint:gosec // G706: msg es un literal del código; los atributos son los que ya registraba cada llamador.
	slog.Warn(msg, args...)
}
