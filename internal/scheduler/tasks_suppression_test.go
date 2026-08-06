package scheduler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/notifications"
	"github.com/neuro-bot/neuro-bot/internal/testutil"
)

type failingDeliverability struct{ failures int }

func (m *failingDeliverability) ConsecutiveFailures(_ context.Context, _ string) (int, error) {
	return m.failures, nil
}

// El recordatorio de día-antes a un número con 2+ fallos de entrega confirmados se suprime:
// era la fuente de los ~591 templates/30d a teléfonos fantasma que jamás respondieron.
func TestSendWhatsAppReminders_SuppressedNumberSkipped(t *testing.T) {
	var posts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posts.Add(1)
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"id":"msg-1"}`))
	}))
	defer srv.Close()

	birdClient := bird.NewClientForTest(srv.URL)
	cfg := testConfig()
	nm := notifications.NewNotificationManager(birdClient, nil, cfg)
	nm.SetDeliverability(&failingDeliverability{failures: 2})

	tomorrow := time.Now().AddDate(0, 0, 1)
	appts := []domain.Appointment{{
		ID: "apt-sup-1", PatientID: "P001", PatientName: "Juan Perez",
		PatientPhone: "3001234567", Date: tomorrow, TimeSlot: tomorrow.Format("200601021504"),
		Procedures: []domain.AppointmentProcedure{{CupCode: "890271", CupName: "EMG", Quantity: 1}},
	}}
	tasks := &Tasks{
		AppointmentRepo: &testutil.MockAppointmentRepo{
			FindPendingByDateFn: func(_ context.Context, _ string) ([]domain.Appointment, error) {
				return appts, nil
			},
		},
		BirdClient:    birdClient,
		NotifyManager: nm,
		Cfg:           cfg,
	}

	if err := tasks.sendWhatsAppReminders(context.Background()); err != nil {
		t.Fatalf("sendWhatsAppReminders: %v", err)
	}
	if posts.Load() != 0 {
		t.Errorf("número suprimido: 0 envíos esperados, hubo %d", posts.Load())
	}
}
