package notifications

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/config"
	"github.com/neuro-bot/neuro-bot/internal/services"
	"github.com/neuro-bot/neuro-bot/internal/session"
)

// errActiveSessionExists reproduce el error del índice único de sesión activa (uq_active_phone).
var errActiveSessionExists = errors.New("active session already exists for phone")

// mockSessionCreatorWithLookup extiende el mock base con la búsqueda de la sesión que YA ocupa el cupo
// del teléfono, y rechaza el Create como lo hace la BD real cuando esa sesión existe.
type mockSessionCreatorWithLookup struct {
	mu       sync.Mutex
	existing *session.Session

	created      *session.Session
	createCalls  int
	statusCalls  []string
	batchSession string
}

func (m *mockSessionCreatorWithLookup) Create(_ context.Context, s *session.Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.createCalls++
	if m.existing != nil {
		return errActiveSessionExists // la BD real rechaza la segunda sesión activa
	}
	m.created = s
	return nil
}

func (m *mockSessionCreatorWithLookup) FindCurrentByPhone(_ context.Context, _ string) (*session.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.existing, nil
}

func (m *mockSessionCreatorWithLookup) SetContextBatch(_ context.Context, sessionID string, _ map[string]string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.batchSession = sessionID
	return nil
}

func (m *mockSessionCreatorWithLookup) UpdateStatus(_ context.Context, sessionID, status string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.statusCalls = append(m.statusCalls, sessionID+":"+status)
	return nil
}

func (m *mockSessionCreatorWithLookup) CompleteActiveByPhone(_ context.Context, _ string) error {
	return nil
}

func escalationBirdServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/agents"):
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"results":[{"id":"agent-1","name":"Test Agent","teams":[{"id":"team-fallback","name":"CC"}],"availability":{"status":"active","activity":"available"},"rootItemAssignedCount":0}]}`))
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/search/feed-items"):
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"results":[{"id":"fi-conv-1","feedId":"channel:ch-test","closed":false}]}`))
		default:
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"id":"ok"}`))
		}
	}))
}

// TestEscalateNotifToAgent_ReusesExistingSession cubre el bug de producción visto el 2026-07-21 y el
// 2026-07-26 ("escalateNotifToAgent: create session — active session already exists for phone").
//
// Si el paciente YA tiene una sesión ocupando el cupo del teléfono, crear otra choca con el índice
// único. Antes el error solo se logueaba y la escalación seguía con sessID vacío: el handoff a Bird SÍ
// ocurría, pero sin fila en `escalations` ni evento `escalated_to_agent` → sin SLA, sin recordatorio al
// agente y sin detección de no-show; y como la sesión original quedaba intacta, el bot seguía
// respondiéndole al paciente mientras el agente lo atendía (control doble sobre el mismo chat).
//
// Comportamiento exigido: escalar SOBRE la sesión existente.
func TestEscalateNotifToAgent_ReusesExistingSession(t *testing.T) {
	srv := escalationBirdServer()
	defer srv.Close()

	existing := &session.Session{
		ID:          "sess-existente",
		PhoneNumber: "+573001234567",
		Status:      session.StatusActive,
	}
	sessRepo := &mockSessionCreatorWithLookup{existing: existing}

	mgr := NewNotificationManager(bird.NewClientForTest(srv.URL),
		services.NewAppointmentService(&mockApptRepoNotif{}, nil),
		&config.Config{BirdTeamFallback: "team-fallback"})
	mgr.SetWaitingListDeps(nil, sessRepo, &mockVirtualEnqueuer{})
	tracker := &mockEventLoggerNotif{}
	mgr.SetTracker(tracker)
	rec := &mockEscRecorderNotif{}
	mgr.SetEscalationRecorder(rec)

	mgr.escalateNotifToAgent(&PendingNotification{
		Phone: "+573001234567", AppointmentID: "APT001", Type: "confirmation",
	}, "conv-1")

	sessRepo.mu.Lock()
	batch, statuses, createCalls := sessRepo.batchSession, append([]string{}, sessRepo.statusCalls...), sessRepo.createCalls
	sessRepo.mu.Unlock()

	if createCalls > 0 {
		t.Errorf("no debe intentar crear una segunda sesión activa; Create se llamó %d veces", createCalls)
	}
	if batch != "sess-existente" {
		t.Errorf("el contexto de la notificación debe ir a la sesión existente, got %q", batch)
	}
	found := false
	for _, s := range statuses {
		if s == "sess-existente:"+session.StatusEscalated {
			found = true
		}
	}
	if !found {
		t.Errorf("la sesión existente debe quedar en estado escalado (si no, el bot le sigue respondiendo al paciente); got %v", statuses)
	}

	// Lo crítico: la escalación queda REGISTRADA (SLA, recordatorio al agente, no-show).
	rec.mu.Lock()
	created := rec.created
	rec.mu.Unlock()
	if created != 1 {
		t.Errorf("esperaba 1 fila en escalations, got %d — sin ella no hay SLA ni recordatorio al agente", created)
	}
	tracker.mu.Lock()
	evs := append([]string{}, tracker.events...)
	tracker.mu.Unlock()
	hasEsc := false
	for _, e := range evs {
		if e == "escalated_to_agent" {
			hasEsc = true
		}
	}
	if !hasEsc {
		t.Errorf("esperaba escalated_to_agent emitido; got %v", evs)
	}
}
