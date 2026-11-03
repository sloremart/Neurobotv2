package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/config"
	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/notifications"
	"github.com/neuro-bot/neuro-bot/internal/services"
)

// siesaRows es un SiesaPendingStore en memoria.
type siesaRows struct {
	mu       sync.Mutex
	rows     []notifications.SiesaPendingRow
	outcomes map[int64]string
}

func (s *siesaRows) MarkSiesaPending(_ context.Context, row notifications.SiesaPendingRow, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	row.ID = int64(len(s.rows) + len(s.outcomes) + 1)
	s.rows = append(s.rows, row)
	return nil
}

func (s *siesaRows) FindSiesaPending(_ context.Context, _ int) ([]notifications.SiesaPendingRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]notifications.SiesaPendingRow(nil), s.rows...), nil
}

func (s *siesaRows) RecordSiesaAttempt(context.Context, int64) error { return nil }

func (s *siesaRows) FinishSiesaPending(_ context.Context, id int64, outcome string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, r := range s.rows {
		if r.ID == id {
			s.rows = append(s.rows[:i], s.rows[i+1:]...)
			s.outcomes[id] = outcome
			return true, nil
		}
	}
	return false, nil
}

// D-04: el aviso de confirmación que manda el dashboard guarda el horario de la cita; si el paciente
// confirma con SIESA caída y la cita se mueve (mismo id, otra fecha) antes de que SIESA vuelva, la
// pasada no la confirma ni anuncia la fecha nueva: escala.
func TestNotifyConfirmation_DeferredOnMovedAppointmentEscalates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"msg-bird","results":[]}`))
	}))
	t.Cleanup(srv.Close)
	client := bird.NewClientForTest(srv.URL)

	var siesaDown atomic.Bool
	var confirms atomic.Int32
	original := adminAppt("A1")
	moved := original
	moved.TimeSlot = "202607110800"
	var current atomic.Pointer[domain.Appointment]
	current.Store(&original)
	repo := &mockApptRepoAPI{
		findByIDFn: func(context.Context, string) (*domain.Appointment, error) {
			if siesaDown.Load() {
				return nil, errors.New("siesa: connection refused")
			}
			a := *current.Load()
			return &a, nil
		},
		confirmBatchFn: func(context.Context, []string, string, string) error {
			confirms.Add(1)
			return nil
		},
	}
	cfg := &config.Config{CenterName: "Centro", BirdTemplateConfirmProjectID: "proj-confirm"}
	nm := notifications.NewNotificationManager(client, services.NewAppointmentService(repo, nil), cfg)
	store := &siesaRows{outcomes: map[int64]string{}}
	nm.SetSiesaPendingStore(store)
	events := &safeEvents{}
	nm.SetTracker(events)
	h := &InternalHandler{appointmentRepo: repo, birdClient: client, notifyManager: nm, tracker: events, cfg: cfg, startTime: time.Now()}

	rig := &adminRig{h: h, nm: nm, events: events}
	if rec := notifyConfirmationRequest(rig); rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if p, ok := nm.LoadPendingForTest(adminPhone); !ok || p.AppointmentSlot != original.TimeSlot {
		t.Fatalf("el pending del dashboard guarda el horario: %+v ok=%v", p, ok)
	}

	siesaDown.Store(true)
	nm.HandleResponse(adminPhone, "confirm", "conv-1")
	if rows, _ := store.FindSiesaPending(context.Background(), 10); len(rows) != 1 || rows[0].AppointmentSlot != original.TimeSlot {
		t.Fatalf("respuesta pendiente con el horario recordado: %+v", rows)
	}

	siesaDown.Store(false)
	current.Store(&moved)
	nm.RetrySiesaPendingForTest(context.Background(), time.Now())
	nm.RetrySiesaPendingForTest(context.Background(), time.Now())

	if n := confirms.Load(); n != 0 {
		t.Fatalf("una cita movida no se confirma: ConfirmBatch = %d", n)
	}
	if got := store.outcomes[1]; got != "not_applicable" {
		t.Fatalf("desenlace = %q, want not_applicable", got)
	}
	escalations := 0
	events.mu.Lock()
	for _, e := range events.events {
		if e.eventType == "notification_escalated_agent" {
			escalations++
		}
	}
	events.mu.Unlock()
	if escalations != 1 {
		t.Fatalf("escalaciones = %d, want 1", escalations)
	}
}
