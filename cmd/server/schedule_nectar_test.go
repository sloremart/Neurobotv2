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
	// globalKey: la lectura del roster responde 403 account_key_required (la clave global).
	globalKey  bool
	agentsHits int
}

func newFakeScheduleServer(t *testing.T, body string) *fakeScheduleServer {
	f := &fakeScheduleServer{body: body, code: http.StatusOK}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == nectar.AgentsPath {
			f.mu.Lock()
			f.agentsHits++
			global := f.globalKey
			f.mu.Unlock()
			if global {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"error":"account_key_required"}`))
				return
			}
			_, _ = w.Write([]byte(`{"agents":[]}`))
			return
		}
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

// holidayMonday es el lunes festivo de withSaturday a las 10:00: hábil en el horario de siempre.
var holidayMonday = time.Date(2026, 11, 2, 10, 0, 0, 0, cot)

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
func TestStartNectarSchedule_SinPuenteNoLlamaANadie(t *testing.T) {
	logs := captureSlog(t)
	cfg := &config.Config{}
	cache := wireSchedule(cfg)

	if startNectarSchedule(context.Background(), cfg, nil, cache) {
		t.Fatal("sin puente no se arranca el refresco")
	}
	if got, want := cfg.Hours().String(), config.DefaultBusinessHours().String(); got != want {
		t.Errorf("sin puente rige el de siempre: %q", got)
	}
	if cfg.Hours().IsHoliday(holidayMonday) {
		t.Error("sin puente no hay festivos")
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

// holidaysOnlyRefresher es el refresco de un bot con puente y NECTAR_SCHEDULE_ENABLED apagada.
func holidaysOnlyRefresher(t *testing.T, f *fakeScheduleServer) (*config.Config, *nectarScheduleRefresher, *config.CachedSchedule) {
	t.Helper()
	cfg, r, cache := refresherFor(t, f)
	r.holidaysOnly = true
	return cfg, r, cache
}

// D-08: con puente y sin el horario de Néctar, los festivos publicados cierran el día, pero la semana
// sigue siendo la de siempre aunque Néctar publique otras ventanas.
func TestHolidaysOnly_AppliesHolidaysOverDefaultWeek(t *testing.T) {
	f := newFakeScheduleServer(t, withSaturday)
	cfg, r, cache := holidaysOnlyRefresher(t, f)
	if err := r.refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	h := cfg.Hours()
	if h.Open(holidayMonday) || !h.IsHoliday(holidayMonday) {
		t.Error("el festivo publicado cierra el lunes")
	}
	if !h.Open(holidayMonday.AddDate(0, 0, 1)) {
		t.Error("el día siguiente abre")
	}
	if got, want := h.String(), config.DefaultBusinessHours().String(); got != want {
		t.Errorf("semana = %q, want la de siempre %q", got, want)
	}
	// 07:30 abre el de siempre; las ventanas publicadas empiezan a las 08:00.
	if !h.Open(time.Date(2026, 11, 3, 7, 30, 0, 0, cot)) {
		t.Error("las ventanas de Néctar no mandan con la bandera apagada")
	}
	if !h.SameAsDefault() {
		t.Error("el texto de ayuda sigue siendo el de siempre")
	}
	if cache.Version() != "v2" {
		t.Errorf("versión = %q: la lectura condicional necesita la de Néctar", cache.Version())
	}
}

// D-08: la bandera apagada nunca publica «cerrado toda la semana», ni con Néctar sin ventanas.
func TestHolidaysOnly_NoWindowsIsNotClosed(t *testing.T) {
	f := newFakeScheduleServer(t, `{"timezone":"America/Bogota","version":"v0","windows":[],
		"holidays":[{"date":"2026-11-02","name":"Todos los Santos"}]}`)
	cfg, r, _ := holidaysOnlyRefresher(t, f)
	logs := captureSlog(t)
	if err := r.refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	h := cfg.Hours()
	if !h.Open(saturday) || !h.Open(holidayMonday.AddDate(0, 0, 1)) || h.Open(holidayMonday) {
		t.Errorf("rige el de siempre con el festivo: horario %q", h.String())
	}
	if h.Describe() == "" {
		t.Error("sigue habiendo horario que nombrar")
	}
	if strings.Contains(logs.String(), "level=ERROR") {
		t.Errorf("no hay cierre que avisar: %s", logs.String())
	}
}

// D-08: Néctar caído, una respuesta rota, una fecha inválida u otra zona conservan la última lista buena
// y dejan un aviso en el log; un 304 también la conserva.
func TestHolidaysOnly_KeepsLastGoodList(t *testing.T) {
	f := newFakeScheduleServer(t, withSaturday)
	cfg, r, cache := holidaysOnlyRefresher(t, f)
	if err := r.refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.publish(`{"timezone":"America/Bogota","version":"v3","windows":[],"holidays":[{"date":"2026-13-40"}]}`)
	if err := r.refresh(context.Background()); err == nil {
		t.Error("un festivo inválido tumba la lectura")
	}
	f.publish(`{"timezone":"America/Lima","version":"v4","windows":[],"holidays":[{"date":"2026-11-16"}]}`)
	if err := r.refresh(context.Background()); err == nil {
		t.Error("otra zona tumba la lectura: sus fechas no serían las de Bogotá")
	}
	f.fail(http.StatusInternalServerError)
	if err := r.refresh(context.Background()); err == nil {
		t.Error("una respuesta 500 es un fallo")
	}
	if h := cfg.Hours(); h.Open(holidayMonday) || !h.Open(saturday) || cache.Version() != "v2" {
		t.Errorf("sigue la última buena: horario %q versión %q", h.String(), cache.Version())
	}
}

// D-08: Néctar sin responder al arrancar: rige el de siempre, sin festivos ni cierre, con su aviso.
func TestHolidaysOnly_NectarDownAtStartKeepsDefault(t *testing.T) {
	f := newFakeScheduleServer(t, withSaturday)
	f.fail(http.StatusServiceUnavailable)
	cfg, r, _ := holidaysOnlyRefresher(t, f)
	logs := captureSlog(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Run(ctx)
	waitFor(t, "el aviso", func() bool { return strings.Contains(logs.String(), "level=WARN") })
	if h := cfg.Hours(); !h.Open(holidayMonday) || !h.Open(saturday) {
		t.Errorf("rige el de siempre: %q", h.String())
	}
}

// D-08: con puente y la bandera apagada el refresco arranca en modo solo festivos; encendida, como hoy.
func TestStartNectarSchedule_ConPuenteLeeSegunLaBandera(t *testing.T) {
	for _, tc := range []struct {
		name         string
		enabled      bool
		openSaturday bool // withoutSaturday no publica sábado: solo manda con la bandera encendida
	}{
		{"apagada: solo festivos", false, true},
		{"encendida: el horario de Néctar", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			captureSlog(t)
			f := newFakeScheduleServer(t, withoutSaturday)
			cfg := &config.Config{NectarScheduleEnabled: tc.enabled}
			cache := wireSchedule(cfg)
			client := nectar.NewClient(f.srv.URL, "clave", "secreto")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if !startNectarSchedule(ctx, cfg, client, cache) {
				t.Fatal("con puente el refresco arranca")
			}
			waitFor(t, "la primera lectura", func() bool { return cache.Version() == "v1" })
			if got := cfg.Hours().Open(saturday); got != tc.openSaturday {
				t.Errorf("abierto el sábado = %v, want %v", got, tc.openSaturday)
			}
		})
	}
}

// M-7: con la clave global Néctar mezclaría los festivos de todas las entidades. Solo festivos exige la
// clave de CUENTA: la comprueba una vez con el roster (que la global no puede leer) y, con la global,
// no aplica nada y se detiene con un solo error.
func TestHolidaysOnly_RequiresAccountKey(t *testing.T) {
	f := newFakeScheduleServer(t, withSaturday)
	f.globalKey = true
	cfg, r, cache := holidaysOnlyRefresher(t, f)
	logs := captureSlog(t)

	done := make(chan struct{})
	go func() { r.Run(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("con la clave global el refresco se detiene")
	}
	if f.calls() != 0 || cache.Version() != "" || cfg.Hours().IsHoliday(holidayMonday) {
		t.Errorf("no se lee ni se aplica ningún festivo: lecturas=%d versión=%q", f.calls(), cache.Version())
	}
	if n := strings.Count(logs.String(), "level=ERROR"); n != 1 || !strings.Contains(logs.String(), "clave de CUENTA") {
		t.Errorf("un solo error que nombre la clave: %s", logs.String())
	}
}

// M-7: la clave se comprueba una sola vez; si Néctar no responde a esa comprobación, no se aplica nada y
// se vuelve a intentar en el tic siguiente.
func TestHolidaysOnly_ChecksAccountKeyOnce(t *testing.T) {
	f := newFakeScheduleServer(t, withSaturday)
	cfg, r, _ := holidaysOnlyRefresher(t, f)
	for range 3 {
		if err := r.refresh(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	f.mu.Lock()
	agents := f.agentsHits
	f.mu.Unlock()
	if agents != 1 || !cfg.Hours().IsHoliday(holidayMonday) {
		t.Errorf("comprobaciones = %d, want 1; festivo aplicado = %v", agents, cfg.Hours().IsHoliday(holidayMonday))
	}
}

// Con NECTAR_SCHEDULE_ENABLED encendida la clave global tampoco vale: no se publica nada (rige la semana
// de siempre sin festivos de Néctar, nunca «cerrado toda la semana») y queda un solo error.
func TestScheduleEnabled_RequiresAccountKey(t *testing.T) {
	f := newFakeScheduleServer(t, `{"timezone":"America/Bogota","version":"v0","windows":[],
		"holidays":[{"date":"2026-11-02","name":"Todos los Santos"}]}`)
	f.globalKey = true
	cfg, r, cache := refresherFor(t, f)
	logs := captureSlog(t)

	done := make(chan struct{})
	go func() { r.Run(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("con la clave global el refresco se detiene")
	}
	h := cfg.Hours()
	if f.calls() != 0 || cache.Version() != "" || h.String() != config.DefaultBusinessHours().String() ||
		!h.Open(saturday) || h.IsHoliday(holidayMonday) {
		t.Errorf("rige la semana de siempre sin festivos: lecturas=%d versión=%q horario=%q", f.calls(), cache.Version(), h.String())
	}
	if n := strings.Count(logs.String(), "level=ERROR"); n != 1 || !strings.Contains(logs.String(), "clave de CUENTA") {
		t.Errorf("un solo error que nombre la clave: %s", logs.String())
	}
}

// G1-M3: la comprobación de la clave queda fijada también con NECTAR_SCHEDULE_ENABLED encendida: el roster
// se lee una vez, no en cada tic junto al horario.
func TestScheduleEnabled_ChecksAccountKeyOnce(t *testing.T) {
	f := newFakeScheduleServer(t, withSaturday)
	_, r, cache := refresherFor(t, f)
	for range 3 {
		if err := r.refresh(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	f.mu.Lock()
	agents := f.agentsHits
	f.mu.Unlock()
	if agents != 1 || f.calls() != 3 || cache.Version() != "v2" {
		t.Errorf("roster leído %d veces (want 1); horario %d veces (want 3); versión %q", agents, f.calls(), cache.Version())
	}
}
