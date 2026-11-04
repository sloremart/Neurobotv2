package api

import (
	"bytes"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/config"
)

func captureWarnLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &logs
}

// TestUnauthenticatedRejections_LogIsCapped: una inundación sin firma ni clave no deja una línea WARN
// por petición en ninguna entrada pública; queda en el tope del muestreador (A-09).
func TestUnauthenticatedRejections_LogIsCapped(t *testing.T) {
	const flood = 200
	cfg := &config.Config{
		BirdWebhookSecret:      "secreto-bird",
		BirdWebhookSecretVoice: "secreto-voz",
		NectarNoticeSecret:     "secreto-nectar",
	}
	h := NewWebhookHandler(bird.NewClient(cfg), nil, nil, cfg)
	internal := InternalAuth("clave-interna")(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	cases := []struct {
		msg     string
		method  string
		path    string
		handler http.HandlerFunc
	}{
		{"invalid webhook signature", http.MethodPost, "/api/webhooks/whatsapp", h.HandleWhatsApp},
		{"invalid conversation webhook signature", http.MethodPost, "/api/webhooks/conversations", h.HandleConversation},
		{"invalid voice webhook signature", http.MethodPost, "/api/webhooks/voice", h.HandleVoiceWebhook},
		{"invalid voice dtmf webhook signature", http.MethodPost, "/api/webhooks/voice/dtmf", h.HandleVoiceDTMF},
		{"nectar: aviso rechazado", http.MethodPost, "/api/webhooks/nectar", h.HandleNectarNotice},
		{"nectar: capacidades rechazadas", http.MethodGet, NectarCapabilitiesPath, h.HandleNectarCapabilities},
		{"security: invalid API key", http.MethodGet, "/api/internal/sessions", internal.ServeHTTP},
	}
	for _, tc := range cases {
		logs := captureWarnLog(t)
		for range flood {
			rec := httptest.NewRecorder()
			tc.handler(rec, httptest.NewRequest(tc.method, tc.path, strings.NewReader(`{}`)))
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("%s: código %d, quiero 401", tc.path, rec.Code)
			}
		}
		if got := strings.Count(logs.String(), tc.msg); got == 0 || got > authLogBurst {
			t.Errorf("%s: %d rechazos dejaron %d líneas %q; quiero entre 1 y %d", tc.path, flood, got, tc.msg, authLogBurst)
		}
	}
}

// TestRateLimiter_ExceededLogIsCapped: pasado el límite, cada petición rechazada tampoco deja su línea.
func TestRateLimiter_ExceededLogIsCapped(t *testing.T) {
	logs := captureWarnLog(t)
	if got := rateLimitHits(t, 1, nil, "203.0.113.7:40001", xffSeries(200, "198.51.100.%d")); got != 199 {
		t.Fatalf("esperaba 199 respuestas 429, got %d", got)
	}
	if got := strings.Count(logs.String(), "rate limit exceeded"); got == 0 || got > authLogBurst {
		t.Errorf("199 rechazos dejaron %d líneas; quiero entre 1 y %d", got, authLogBurst)
	}
}

// TestLogSampler_ReportsSuppressedInNextWindow: lo descartado no desaparece sin rastro: la primera
// línea de la ventana siguiente dice cuántas se callaron, y cada mensaje lleva su propia cuenta.
func TestLogSampler_ReportsSuppressedInNextWindow(t *testing.T) {
	logs := captureWarnLog(t)
	now := time.Date(2026, 11, 4, 8, 0, 0, 0, time.UTC)
	s := newAuthLogSampler()
	s.now = func() time.Time { return now }
	s.after = func(time.Duration, func()) {} // sin resumen: aquí se prueba el arrastre a la ventana siguiente

	for range authLogBurst + 5 {
		s.Warn("rechazo A")
	}
	s.Warn("rechazo B")
	now = now.Add(authLogWindow)
	s.Warn("rechazo A", "k", "v")

	if got := strings.Count(logs.String(), "rechazo A"); got != authLogBurst+1 {
		t.Errorf("líneas de A = %d, quiero %d", got, authLogBurst+1)
	}
	if !strings.Contains(logs.String(), `msg="rechazo A" k=v suppressed=5`) {
		t.Errorf("la primera línea de la ventana nueva debe contar las 5 calladas:\n%s", logs.String())
	}
	if got := strings.Count(logs.String(), "rechazo B"); got != 1 {
		t.Errorf("otro mensaje no comparte el cupo: líneas de B = %d", got)
	}
}

// TestRequestLogger_RejectedRequestsAreCapped: la línea «request» de cada 401 también va con tope; las
// respuestas 2xx se registran todas, como siempre (A-09).
func TestRequestLogger_RejectedRequestsAreCapped(t *testing.T) {
	logs := captureWarnLog(t)
	handler := RequestLogger(InternalAuth("clave-interna")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})))
	for range 200 {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/internal/sessions", nil))
	}
	if got := strings.Count(logs.String(), "msg=request"); got == 0 || got > authLogBurst {
		t.Errorf("200 respuestas 401 dejaron %d líneas «request»; quiero entre 1 y %d", got, authLogBurst)
	}

	logs.Reset()
	for range 20 {
		req := httptest.NewRequest(http.MethodGet, "/api/internal/sessions", nil)
		req.Header.Set("X-API-Key", "clave-interna")
		handler.ServeHTTP(httptest.NewRecorder(), req)
	}
	if got := strings.Count(logs.String(), "msg=request"); got != 20 {
		t.Errorf("20 respuestas autenticadas dejaron %d líneas «request»; quiero 20", got)
	}
	if !strings.Contains(logs.String(), "status=204") {
		t.Errorf("la línea «request» lleva el código de la respuesta:\n%s", logs.String())
	}
}

// TestLogSampler_SummaryAtWindowEnd: un ataque corto seguido de silencio deja su total en el log al
// cerrar la ventana, sin esperar al próximo rechazo.
func TestLogSampler_SummaryAtWindowEnd(t *testing.T) {
	logs := captureWarnLog(t)
	s := newAuthLogSampler()
	now := time.Date(2026, 11, 4, 8, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	var flush func()
	var waited time.Duration
	s.after = func(d time.Duration, f func()) { waited, flush = d, f }

	for range authLogBurst + 7 {
		s.Warn("rechazo A")
	}
	if flush == nil {
		t.Fatal("la primera línea callada no programó el resumen")
	}
	if waited != authLogWindow {
		t.Errorf("el resumen se programó a %v, quiero el fin de la ventana (%v)", waited, authLogWindow)
	}
	flush()
	if !strings.Contains(logs.String(), `msg="rechazo A" suppressed=7`) {
		t.Errorf("el resumen debe contar las 7 calladas:\n%s", logs.String())
	}
	// La ventana siguiente no repite el total que ya salió en el resumen.
	now = now.Add(authLogWindow)
	s.Warn("rechazo A")
	if got := strings.Count(logs.String(), "suppressed="); got != 1 {
		t.Errorf("el total salió %d veces, quiero 1:\n%s", got, logs.String())
	}
}

// TestRequestLogger_ErrorFloodIsCapped: cualquier código 4xx se puede provocar sin límite (404 de rutas
// inventadas, 405…): su línea «request» va con tope, una cuenta por código.
func TestRequestLogger_ErrorFloodIsCapped(t *testing.T) {
	logs := captureWarnLog(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/webhooks/nectar", func(http.ResponseWriter, *http.Request) {})
	handler := RequestLogger(mux)
	for i := range 200 {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, fmt.Sprintf("/no-existe-%d", i), nil))
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/webhooks/nectar", nil))
	}
	for _, code := range []string{"status=404", "status=405"} {
		if got := strings.Count(logs.String(), code); got == 0 || got > authLogBurst {
			t.Errorf("200 respuestas %s dejaron %d líneas; quiero entre 1 y %d", code, got, authLogBurst)
		}
	}
}

// R2-1: los 5xx no los provoca un anónimo a voluntad y son lo que se mira en una caída: todos dejan su
// línea «request».
func TestRequestLogger_ServerErrorsAreNotCapped(t *testing.T) {
	logs := captureWarnLog(t)
	handler := RequestLogger(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	const n = 3 * authLogBurst
	for range n {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/internal/x", nil))
	}
	if got := strings.Count(logs.String(), "status=500"); got != n {
		t.Fatalf("%d respuestas 500 dejaron %d líneas", n, got)
	}
}
