package api

import (
	"bytes"
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
