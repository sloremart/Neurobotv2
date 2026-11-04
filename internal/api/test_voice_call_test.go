package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/config"
	"github.com/neuro-bot/neuro-bot/internal/notifications"
)

// testVoiceRig monta test-voice-call contra un Bird falso, con un recordatorio REAL pendiente para
// el mismo teléfono.
func testVoiceRig(t *testing.T, cfg *config.Config, phone string) (*InternalHandler, *notifications.NotificationManager, *fakeVoiceBird) {
	t.Helper()
	fake := &fakeVoiceBird{}
	client := bird.NewVoiceClientForTest(fake.server(t).URL)
	nm := notifications.NewNotificationManager(client, nil, cfg)
	if !nm.RegisterPending(notifications.PendingNotification{Type: "confirmation", Phone: phone, AppointmentID: "A1"}) {
		t.Fatal("no se registró el pending")
	}
	t.Cleanup(func() {
		if p, ok := nm.LoadPendingForTest(phone); ok && p.Timer != nil {
			p.Timer.Stop()
		}
	})
	return &InternalHandler{cfg: cfg, birdClient: client, notifyManager: nm}, nm, fake
}

func postTestVoiceCall(h *InternalHandler, phone string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.HandleTestVoiceCall(rec, httptest.NewRequest(http.MethodPost, "/api/internal/test-voice-call",
		strings.NewReader(`{"phone":"`+phone+`"}`)))
	return rec
}

// TestHandleTestVoiceCall_DoesNotBindRealPending: la llamada de prueba no se ata al recordatorio real
// del teléfono; su DTMF (confirmar u otra tecla) no toca la cita ni el pending (A-07, B-11).
func TestHandleTestVoiceCall_DoesNotBindRealPending(t *testing.T) {
	const phone = "+573001112233"
	for _, keys := range []string{"1", "2", ""} {
		h, nm, _ := testVoiceRig(t, &config.Config{CenterName: "Centro"}, phone)
		errs := captureAdminErrors(t)

		if rec := postTestVoiceCall(h, phone); rec.Code != http.StatusOK {
			t.Fatalf("test-voice-call: código %d %s", rec.Code, rec.Body.String())
		}
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("tecla %q: el DTMF de la prueba llegó a la cita real (%v)", keys, r)
				}
			}()
			nm.HandleVoiceGatherResult("call-test-1", keys)
			nm.HandleVoiceCallCompleted("call-test-1")
		}()

		p, ok := nm.LoadPendingForTest(phone)
		if !ok {
			t.Fatalf("tecla %q: el DTMF de la prueba consumió el recordatorio real", keys)
		}
		if p.CallID != "" {
			t.Errorf("tecla %q: la llamada de prueba quedó atada al pending real (CallID=%q)", keys, p.CallID)
		}
		if lines := errs.errors(); len(lines) > 0 {
			t.Errorf("tecla %q: la llamada de prueba deja errores en el log: %v", keys, lines)
		}
	}
}

// TestHandleTestVoiceCall_RespectsTestingWhitelist: con TESTING_WHITELIST_PHONES puesta, la prueba
// solo llama a esos números, como el resto de los envíos salientes.
func TestHandleTestVoiceCall_RespectsTestingWhitelist(t *testing.T) {
	cfg := &config.Config{CenterName: "Centro", TestingWhitelistPhones: []string{"+573009990000"}}
	h, _, fake := testVoiceRig(t, cfg, "+573001112233")
	if rec := postTestVoiceCall(h, "+573001112233"); rec.Code != http.StatusForbidden {
		t.Fatalf("teléfono fuera de la lista: código %d, quiero 403", rec.Code)
	}
	if fake.calls.Load() != 0 {
		t.Fatal("se llamó a un teléfono fuera de la lista")
	}
}
