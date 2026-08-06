package notifications

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/config"
)

// Los followups 1/2 son texto libre: WhatsApp solo los entrega dentro de la ventana de servicio
// (24h desde el ÚLTIMO mensaje del paciente). Si el paciente nunca respondió al template, la
// ventana está cerrada: el envío se cobra y no llega. El gate debe saltarse el texto (la cadena
// continúa hacia IVR/escalación, que sí funcionan sin ventana).

func windowGateManager(t *testing.T, lastMsg *time.Time) (*NotificationManager, *atomic.Int32, func()) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			hits.Add(1)
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"id":"x"}`))
	}))
	cfg := &config.Config{
		ConfirmFollowupEnabled: true,
		ConfirmFollowup1Hours:  3,
		ConfirmFollowup2Hours:  3,
		ConfirmPostIVRMinutes:  30,
	}
	mgr := NewNotificationManager(bird.NewClientForTest(srv.URL), nil, cfg)
	mgr.SetWaitingListDeps(nil, &mockSessionCreator{lastPatientMsgAt: lastMsg}, nil)
	return mgr, &hits, srv.Close
}

func TestHandleTimeout_Followup1_SkippedWhenWindowClosed(t *testing.T) {
	mgr, hits, closeSrv := windowGateManager(t, nil) // el paciente nunca escribió
	defer closeSrv()

	mgr.pending.Store("+573001234567", &PendingNotification{
		Type: "confirmation", Phone: "+573001234567", RetryCount: 0,
	})
	mgr.handleTimeout("+573001234567")

	if hits.Load() != 0 {
		t.Errorf("ventana cerrada: el followup 1 no debe enviarse (se cobra y no entrega), hubo %d envíos", hits.Load())
	}
	val, _ := mgr.pending.Load("+573001234567")
	if p := val.(*PendingNotification); p.RetryCount != 1 {
		t.Errorf("la cadena debe continuar (RetryCount 1), got %d", p.RetryCount)
	}
}

func TestHandleTimeout_Followup1_SentWhenWindowOpen(t *testing.T) {
	recent := time.Now().Add(-1 * time.Hour)
	mgr, hits, closeSrv := windowGateManager(t, &recent)
	defer closeSrv()

	mgr.pending.Store("+573001234567", &PendingNotification{
		Type: "confirmation", Phone: "+573001234567", RetryCount: 0,
	})
	mgr.handleTimeout("+573001234567")

	if hits.Load() != 1 {
		t.Errorf("ventana abierta: el followup 1 debe enviarse, hubo %d envíos", hits.Load())
	}
}

func TestHandleTimeout_Followup2_SkippedWhenWindowClosed(t *testing.T) {
	stale := time.Now().Add(-30 * time.Hour) // escribió hace más de 24h: ventana cerrada
	mgr, hits, closeSrv := windowGateManager(t, &stale)
	defer closeSrv()

	mgr.pending.Store("+573001234567", &PendingNotification{
		Type: "confirmation", Phone: "+573001234567", RetryCount: 1,
	})
	mgr.handleTimeout("+573001234567")

	if hits.Load() != 0 {
		t.Errorf("ventana cerrada: el followup 2 no debe enviarse, hubo %d envíos", hits.Load())
	}
	val, _ := mgr.pending.Load("+573001234567")
	if p := val.(*PendingNotification); p.RetryCount != 2 {
		t.Errorf("la cadena debe continuar (RetryCount 2), got %d", p.RetryCount)
	}
}

// Sin sessionRepo (o con error de BD) el gate NO debe silenciar los followups: fail-open.
func TestWindowOpen_FailOpenWithoutRepo(t *testing.T) {
	cfg := &config.Config{}
	mgr := NewNotificationManager(nil, nil, cfg)
	if !mgr.WindowOpen(t.Context(), "+573001234567") {
		t.Error("sin sessionRepo el gate debe ser fail-open (true)")
	}
}
