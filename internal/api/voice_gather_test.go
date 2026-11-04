package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/config"
	"github.com/neuro-bot/neuro-bot/internal/notifications"
)

// fakeVoiceBird cuenta las llamadas y los gather que recibe el Bird falso.
type fakeVoiceBird struct {
	calls, gathers atomic.Int32
}

func (f *fakeVoiceBird) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/gather"):
			f.gathers.Add(1)
			_, _ = w.Write([]byte(`{"id":"cmd-1"}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/calls"):
			f.calls.Add(1)
			_, _ = w.Write([]byte(`{"id":"call-test-1"}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestVoiceWebhook_OngoingDuplicateSendsGatherOnce: Bird puede reentregar el mismo «ongoing»; cada
// gather se cobra, así que una llamada recibe uno solo (A-05).
func TestVoiceWebhook_OngoingDuplicateSendsGatherOnce(t *testing.T) {
	fake := &fakeVoiceBird{}
	client := bird.NewVoiceClientForTest(fake.server(t).URL)
	cfg := &config.Config{}
	h := NewWebhookHandler(client, nil, notifications.NewNotificationManager(client, nil, cfg), cfg)

	body := `{"service":"channels","event":"voice.outbound","payload":{"id":"call-1","status":"ongoing"}}`
	for range 2 {
		rec := httptest.NewRecorder()
		h.HandleVoiceWebhook(rec, httptest.NewRequest(http.MethodPost, "/api/webhooks/voice", strings.NewReader(body)))
		if rec.Code != http.StatusOK {
			t.Fatalf("código %d", rec.Code)
		}
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		val, ok := h.voiceGatherCmds.Load("call-1")
		if ok && val.(gatherEntry).commandID == "cmd-1" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("el gather no quedó registrado")
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond) // margen para un segundo envío tardío
	if got := fake.gathers.Load(); got != 1 {
		t.Fatalf("dos «ongoing» de la misma llamada: quiero 1 gather, got %d", got)
	}
}

// TestVoiceWebhook_CompletedBeforeGatherIDTreatsAsNoGather: si la llamada termina mientras el gather
// todavía no tiene id, no se consulta un comando vacío (ni se registra como fallo): se trata como
// llamada sin gather.
func TestVoiceWebhook_CompletedBeforeGatherIDTreatsAsNoGather(t *testing.T) {
	fake := &fakeVoiceBird{}
	client := bird.NewVoiceClientForTest(fake.server(t).URL)
	cfg := &config.Config{}
	h := NewWebhookHandler(client, nil, notifications.NewNotificationManager(client, nil, cfg), cfg)
	h.voiceGatherCmds.Store("call-2", gatherEntry{storedAt: time.Now()}) // gather en vuelo, sin id aún
	errs := captureAdminErrors(t)

	body := `{"service":"channels","event":"voice.outbound","payload":{"id":"call-2","status":"completed"}}`
	h.HandleVoiceWebhook(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/webhooks/voice", strings.NewReader(body)))

	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, ok := h.voiceGatherCmds.Load("call-2"); !ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("el «completed» no consumió la entrada")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if lines := errs.errors(); len(lines) > 0 {
		t.Fatalf("una llamada sin gather no es un error: %v", lines)
	}
}
