package main

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/config"
	"github.com/neuro-bot/neuro-bot/internal/nectar"
)

// logSink guarda lo que el refrescador escribe en slog: los tests no ensucian la salida y pueden
// mirar el aviso que importa. Seguro para la goroutine de Run.
type logSink struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *logSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *logSink) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// captureSlog desvía slog al sumidero mientras dure el test.
func captureSlog(t *testing.T) *logSink {
	t.Helper()
	sink := &logSink{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(sink, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return sink
}

// EL REFRESCO DEL HORARIO DESDE NÉCTAR (port de hdg-bot, cmd/server/schedule_nectar_test.go).
//
// Lo que se protege son las tres reglas que hacen seguro encenderlo: un horario nuevo cambia la
// DECISIÓN sin reiniciar, una lectura fallida conserva la última buena, y con la bandera apagada
// no se llama a nadie.

type fakeScheduleServer struct {
	srv   *httptest.Server
	mu    sync.Mutex
	body  string
	code  int
	hits  int
	conds []string // el If-None-Match de cada llamada ("" = sin cabecera)
}

func newFakeScheduleServer(t *testing.T, body string) *fakeScheduleServer {
	f := &fakeScheduleServer{body: body, code: http.StatusOK}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != nectar.SchedulePath {
			t.Errorf("ruta inesperada: %s", r.URL.Path)
		}
		f.mu.Lock()
		f.hits++
		f.conds = append(f.conds, r.Header.Get("If-None-Match"))
		code, body := f.code, f.body
		f.mu.Unlock()
		if code != http.StatusOK {
			w.WriteHeader(code)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeScheduleServer) lastCond() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.conds) == 0 {
		return ""
	}
	return f.conds[len(f.conds)-1]
}

func (f *fakeScheduleServer) publish(body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.body = body
}

func (f *fakeScheduleServer) fail(code int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.code = code
}

func (f *fakeScheduleServer) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits
}

// Lunes a viernes SIN sábado, y el mismo con sábado y un festivo: la diferencia se ve en el gate
// un sábado por la mañana (el respaldo de Neuro sí abre los sábados).
const (
	withoutSaturday = `{"timezone":"America/Bogota","version":"v1","windows":[
		{"weekday":1,"from":"08:00","to":"17:00"},{"weekday":2,"from":"08:00","to":"17:00"},
		{"weekday":3,"from":"08:00","to":"17:00"},{"weekday":4,"from":"08:00","to":"17:00"},
		{"weekday":5,"from":"08:00","to":"17:00"}]}`
	withSaturday = `{"timezone":"America/Bogota","version":"v2","windows":[
		{"weekday":1,"from":"08:00","to":"17:00"},{"weekday":2,"from":"08:00","to":"17:00"},
		{"weekday":3,"from":"08:00","to":"17:00"},{"weekday":4,"from":"08:00","to":"17:00"},
		{"weekday":5,"from":"08:00","to":"17:00"},{"weekday":6,"from":"08:00","to":"11:00"}],
		"holidays":[{"date":"2026-11-02","name":"Todos los Santos"}]}`
)

var cot = time.FixedZone("COT", -5*3600)

// saturday es un sábado a las 09:00: dentro de la ventana del sábado, fuera de entre semana.
var saturday = time.Date(2026, 10, 31, 9, 0, 0, 0, cot)

func refresherFor(t *testing.T, f *fakeScheduleServer) (*config.Config, *nectarScheduleRefresher, *config.CachedSchedule) {
	t.Helper()
	captureSlog(t)
	cfg := &config.Config{}
	cache := wireSchedule(cfg)
	client := nectar.NewClient(f.srv.URL, "clave", "secreto")
	return cfg, newNectarScheduleRefresher(client, cache, 0, 20*time.Millisecond), cache
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no llegó: %s", what)
}

// La regla principal: lo que publique Néctar cambia la decisión del bot sin reiniciarlo.
func TestNectarScheduleRefresher_CambiaLaDecisionSinReiniciar(t *testing.T) {
	f := newFakeScheduleServer(t, withoutSaturday)
	cfg, r, cache := refresherFor(t, f)

	if !cfg.Hours().Open(saturday) {
		t.Fatal("antes de leer rige el respaldo, que abre el sábado")
	}
	if err := r.refresh(context.Background()); err != nil {
		t.Fatalf("primera lectura: %v", err)
	}
	if cfg.Hours().Open(saturday) || cache.Version() != "v1" {
		t.Errorf("con lo publicado (sin sábado) no se atiende el sábado; versión %q", cache.Version())
	}

	f.publish(withSaturday)
	if err := r.refresh(context.Background()); err != nil {
		t.Fatalf("segunda lectura: %v", err)
	}
	if !cfg.Hours().Open(saturday) || cache.Version() != "v2" {
		t.Errorf("tras publicar el sábado se atiende el sábado; versión %q", cache.Version())
	}
	if cfg.Hours().Open(time.Date(2026, 11, 2, 10, 0, 0, 0, cot)) {
		t.Error("el festivo publicado cierra el lunes entero")
	}
}

// La ruta caída, una respuesta que no se entiende o una ventana inválida NO dejan al bot sin
// horario: conserva la última lectura buena.
func TestNectarScheduleRefresher_ConservaLaUltimaBuena(t *testing.T) {
	f := newFakeScheduleServer(t, withSaturday)
	cfg, r, cache := refresherFor(t, f)
	if err := r.refresh(context.Background()); err != nil {
		t.Fatalf("primera lectura: %v", err)
	}
	cases := []struct {
		name    string
		breakIt func()
	}{
		{"la ruta se cae", func() { f.fail(http.StatusInternalServerError) }},
		{"canal ajeno", func() { f.fail(http.StatusNotFound) }},
		{"otra zona horaria", func() {
			f.fail(http.StatusOK)
			f.publish(`{"timezone":"UTC","version":"v9","windows":[{"weekday":1,"from":"07:00","to":"17:00"}]}`)
		}},
		{"una ventana inválida", func() {
			f.publish(`{"timezone":"America/Bogota","version":"v9","windows":[{"weekday":9,"from":"07:00","to":"17:00"}]}`)
		}},
		{"JSON roto", func() { f.publish(`{"timezone":`) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.breakIt()
			if err := r.refresh(context.Background()); err == nil {
				t.Fatal("una lectura mala tiene que devolver error")
			}
			if cache.Version() != "v2" || !cfg.Hours().Open(saturday) {
				t.Errorf("rige la última buena: versión %q", cache.Version())
			}
		})
	}
}

// Lectura condicional: la primera va pelada, las siguientes citan la versión, y un 304 conserva
// el horario y refresca la marca.
func TestNectarScheduleRefresher_CondicionalYSinCambios(t *testing.T) {
	f := newFakeScheduleServer(t, withoutSaturday)
	cfg, r, cache := refresherFor(t, f)

	if err := r.refresh(context.Background()); err != nil {
		t.Fatalf("primera lectura: %v", err)
	}
	if f.lastCond() != "" {
		t.Errorf("la primera lectura no cita versión: %q", f.lastCond())
	}
	loaded := cache.LoadedAt()

	f.fail(http.StatusNotModified)
	if err := r.refresh(context.Background()); err != nil {
		t.Fatalf("un 304 no es un fallo: %v", err)
	}
	if want := `"v1"`; f.lastCond() != want {
		t.Errorf("If-None-Match = %q, want %s", f.lastCond(), want)
	}
	if cache.Version() != "v1" || cfg.Hours().Open(saturday) || cache.LoadedAt().Before(loaded) {
		t.Errorf("un 304 conserva el horario: versión %q", cache.Version())
	}

	f.fail(http.StatusOK)
	f.publish(withSaturday)
	if err := r.refresh(context.Background()); err != nil {
		t.Fatalf("tercera lectura: %v", err)
	}
	if cache.Version() != "v2" || !cfg.Hours().Open(saturday) {
		t.Errorf("la versión nueva se aplica: %q", cache.Version())
	}
}

// El bucle: lee al arrancar y relee en cada tic.
func TestNectarScheduleRefresher_RunReleeEnCadaTic(t *testing.T) {
	f := newFakeScheduleServer(t, withoutSaturday)
	cfg, r, _ := refresherFor(t, f)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()

	waitFor(t, "la primera lectura", func() bool { return f.calls() > 0 && !cfg.Hours().Open(saturday) })
	f.publish(withSaturday)
	waitFor(t, "el horario republicado", func() bool { return cfg.Hours().Open(saturday) })
	cancel()
	<-done
}

// Apagada (el default) no se llama a Néctar y rige el horario de siempre; encendida sin cliente,
// tampoco.
func TestStartNectarSchedule_ApagadaNoLlamaANadie(t *testing.T) {
	logs := captureSlog(t)
	f := newFakeScheduleServer(t, withoutSaturday)
	cfg := &config.Config{}
	cache := wireSchedule(cfg)
	client := nectar.NewClient(f.srv.URL, "clave", "secreto")

	if startNectarSchedule(context.Background(), cfg, client, cache) {
		t.Fatal("con NECTAR_SCHEDULE_ENABLED apagada no se arranca el refresco")
	}
	time.Sleep(50 * time.Millisecond)
	if f.calls() != 0 {
		t.Errorf("llamadas = %d: apagada no se llama a nadie", f.calls())
	}
	if got, want := cfg.Hours().String(), config.DefaultBusinessHours().String(); got != want {
		t.Errorf("apagada rige el de siempre: %q", got)
	}

	cfg.NectarScheduleEnabled = true
	if startNectarSchedule(context.Background(), cfg, nil, cache) {
		t.Fatal("sin cliente de Néctar no hay de dónde leer el horario")
	}
	// Los dos casos lo dicen en el log: apagada (Info) y encendida sin puente (Error).
	out := logs.String()
	if !strings.Contains(out, "NECTAR_SCHEDULE_ENABLED apagada") ||
		!strings.Contains(out, "level=ERROR msg=\"NECTAR_SCHEDULE_ENABLED encendida sin puente") {
		t.Errorf("log = %s", out)
	}
}

// Néctar publicando CERO ventanas es válido pero deja al bot cerrado: se aplica y se avisa en ERROR.
func TestNectarScheduleRefresher_SinVentanasAvisaEnError(t *testing.T) {
	f := newFakeScheduleServer(t, `{"timezone":"America/Bogota","version":"v0","windows":[]}`)
	cfg, r, _ := refresherFor(t, f)
	logs := captureSlog(t)
	if err := r.refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if cfg.Hours().Open(saturday) || cfg.Hours().Open(saturday.AddDate(0, 0, 3)) {
		t.Error("sin ventanas el bot queda cerrado")
	}
	if !strings.Contains(logs.String(), "level=ERROR") || !strings.Contains(logs.String(), "SIN ventanas") {
		t.Errorf("el cierre total se avisa en ERROR: %s", logs.String())
	}
}

// Encendida sí arranca (con el intervalo 0 cayendo en el default, no en un bucle sin pausa).
func TestStartNectarSchedule_EncendidaLee(t *testing.T) {
	captureSlog(t)
	f := newFakeScheduleServer(t, withSaturday)
	cfg := &config.Config{NectarScheduleEnabled: true}
	cache := wireSchedule(cfg)
	client := nectar.NewClient(f.srv.URL, "clave", "secreto")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if !startNectarSchedule(ctx, cfg, client, cache) {
		t.Fatal("con la bandera encendida y cliente, el refresco arranca")
	}
	waitFor(t, "la primera lectura", func() bool { return cache.Version() == "v2" })
}
