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
	"github.com/neuro-bot/neuro-bot/internal/session"
	"github.com/neuro-bot/neuro-bot/internal/testutil"
)

// stub de notifications.SessionCreator: solo importa LastPatientMessageAt (ventana WA).
type windowSessionStub struct{ last *time.Time }

func (s *windowSessionStub) Create(context.Context, *session.Session) error { return nil }
func (s *windowSessionStub) FindCurrentByPhone(context.Context, string) (*session.Session, error) {
	return nil, nil
}

func (s *windowSessionStub) SetContextBatch(context.Context, string, map[string]string) error {
	return nil
}
func (s *windowSessionStub) UpdateStatus(context.Context, string, string) error  { return nil }
func (s *windowSessionStub) CompleteActiveByPhone(context.Context, string) error { return nil }
func (s *windowSessionStub) LastPatientMessageAt(context.Context, string) (*time.Time, error) {
	return s.last, nil
}

// runRemindersWithPrep corre el recordatorio de día-antes para UN paciente cuyo procedimiento
// tiene preparación, y devuelve cuántos POST llegaron a Bird (template y/o texto de preparación).
func runRemindersWithPrep(t *testing.T, lastPatientMsg *time.Time) int32 {
	t.Helper()
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
	nm.SetWaitingListDeps(nil, &windowSessionStub{last: lastPatientMsg}, nil)

	tomorrow := time.Now().AddDate(0, 0, 1)
	appts := []domain.Appointment{{
		ID: "apt-prep-1", PatientID: "P001", PatientName: "Juan Perez",
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
		ProcedureRepo: &testutil.MockProcedureRepo{
			FindByCodeFn: func(_ context.Context, code string) (*domain.Procedure, error) {
				return &domain.Procedure{Code: code, Preparation: "Ayuno de 8 horas"}, nil
			},
		},
		Cfg: cfg,
	}

	if err := tasks.sendWhatsAppReminders(context.Background()); err != nil {
		t.Fatalf("sendWhatsAppReminders: %v", err)
	}
	return posts.Load()
}

// A las 07:00 el paciente casi nunca ha escrito en 24h: la preparación como texto libre no se
// entrega (WhatsApp la bloquea fuera de ventana) pero Bird la cobra. Con ventana cerrada solo
// debe salir el template; la preparación completa viaja en la respuesta al confirmar.
func TestSendWhatsAppReminders_PrepSkippedWhenWindowClosed(t *testing.T) {
	if got := runRemindersWithPrep(t, nil); got != 1 {
		t.Errorf("ventana cerrada: esperaba 1 POST (solo template), got %d", got)
	}
}

func TestSendWhatsAppReminders_PrepSentWhenWindowOpen(t *testing.T) {
	recent := time.Now().Add(-1 * time.Hour)
	if got := runRemindersWithPrep(t, &recent); got != 2 {
		t.Errorf("ventana abierta: esperaba 2 POST (template + preparación), got %d", got)
	}
}
