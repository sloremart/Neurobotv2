package notifications

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/config"
	"github.com/neuro-bot/neuro-bot/internal/services"
	"github.com/neuro-bot/neuro-bot/internal/session"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
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
	batchCtx     map[string]string
	findCalls    int
	renewed      map[string]time.Time
}

func (m *mockSessionCreatorWithLookup) RenewExpiry(_ context.Context, sessionID string, expiresAt time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.renewed == nil {
		m.renewed = map[string]time.Time{}
	}
	m.renewed[sessionID] = expiresAt
	return nil
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
	m.findCalls++
	return m.existing, nil
}

func (m *mockSessionCreatorWithLookup) SetContextBatch(_ context.Context, sessionID string, kvs map[string]string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.batchSession = sessionID
	m.batchCtx = kvs
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

func (m *mockSessionCreatorWithLookup) LastPatientMessageAt(_ context.Context, _ string) (*time.Time, error) {
	// Ventana abierta por defecto: estos tests cubren la escalación, no el gate de ventana.
	now := time.Now()
	return &now, nil
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

// confirmationEscalationManager arma un manager de Néctar con el repo de sesiones, el tracker y
// el registro de escalaciones a la vista.
func confirmationEscalationManager(t *testing.T, repo *mockSessionCreatorWithLookup) (*NotificationManager, *mockEventLoggerNotif, *mockEscRecorderNotif) {
	t.Helper()
	mgr, _, tracker, rec := confirmationEscalationManagerWithBridge(t, repo)
	return mgr, tracker, rec
}

func confirmationEscalationManagerWithBridge(t *testing.T, repo *mockSessionCreatorWithLookup) (*NotificationManager, *escalationBridge, *mockEventLoggerNotif, *mockEscRecorderNotif) {
	t.Helper()
	mgr, bridge := nectarManager(t, &config.Config{BirdTeamFallback: "team-fallback"})
	mgr.SetWaitingListDeps(nil, repo, &mockVirtualEnqueuer{})
	tracker := &mockEventLoggerNotif{}
	mgr.SetTracker(tracker)
	rec := &mockEscRecorderNotif{}
	mgr.SetEscalationRecorder(rec)
	return mgr, bridge, tracker, rec
}

// P0-2/S25: la escalación por no-respuesta en Néctar no tenía sesión, y el retorno del agente se
// descartaba («agent command but no escalated session»). Ahora crea la sesión ESCALATED en
// NOTIF_PENDING con el contexto de la notificación, y queda registrada.
func TestConfirmationEscalate_NectarCreatesEscalatedSession(t *testing.T) {
	repo := &mockSessionCreatorWithLookup{}
	mgr, tracker, rec := confirmationEscalationManager(t, repo)

	mgr.escalateToAgent(&PendingNotification{
		Phone: "nectar:42", AppointmentID: "APT001", Type: "confirmation", ConversationID: "42",
	}, escalationNoResponse)

	repo.mu.Lock()
	created, kv := repo.created, repo.batchCtx
	repo.mu.Unlock()
	switch {
	case created == nil:
		t.Fatal("la escalación de Néctar debe crear su sesión")
	case created.Status != session.StatusEscalated || created.CurrentState != sm.StateNotifPending:
		t.Errorf("sesión = %s/%s, quiero escalated/NOTIF_PENDING", created.Status, created.CurrentState)
	}
	if kv["pre_escalation_state"] != sm.StateNotifPending ||
		kv[sm.CtxNotifEscalationDetail] != sm.EscalationReasonNotifNoResponse ||
		kv["notif_appointment_id"] != "APT001" || kv["notif_type"] != "confirmation" {
		t.Errorf("contexto incompleto: %v", kv)
	}
	rec.mu.Lock()
	rows := rec.created
	rec.mu.Unlock()
	tracker.mu.Lock()
	evs := append([]string{}, tracker.events...)
	tracker.mu.Unlock()
	if rows != 1 || !slices.Contains(evs, "escalated_to_agent") {
		t.Errorf("la escalación queda registrada: filas=%d eventos=%v", rows, evs)
	}
}

// Review Focus 4: el paciente ya tenía una sesión ACTIVA (ocupa el índice único del teléfono): se
// reutiliza y se marca escalada; un Create chocaría.
func TestConfirmationEscalate_NectarReusesActiveSession(t *testing.T) {
	repo := &mockSessionCreatorWithLookup{existing: &session.Session{
		ID: "sess-activa", PhoneNumber: "nectar:42", Status: session.StatusActive,
	}}
	mgr, _, _ := confirmationEscalationManager(t, repo)

	mgr.escalateToAgent(&PendingNotification{
		Phone: "nectar:42", AppointmentID: "APT001", Type: "confirmation", ConversationID: "42",
	}, escalationNoResponse)

	repo.mu.Lock()
	defer repo.mu.Unlock()
	if repo.createCalls != 0 {
		t.Errorf("Create no se llama con una sesión activa; llamadas=%d", repo.createCalls)
	}
	if !slices.Contains(repo.statusCalls, "sess-activa:"+session.StatusEscalated) {
		t.Errorf("la sesión activa queda escalada: %v", repo.statusCalls)
	}
	if repo.batchSession != "sess-activa" || repo.batchCtx["pre_escalation_state"] != sm.StateNotifPending {
		t.Errorf("el contexto va a la sesión activa: %q %v", repo.batchSession, repo.batchCtx)
	}
}

// Bird no cambia: la escalación de confirmación sigue sin sesión (los comandos del Inbox no la
// necesitan para esta nota).
func TestConfirmationEscalate_BirdCreatesNoSession(t *testing.T) {
	srv := escalationBirdServer()
	defer srv.Close()
	repo := &mockSessionCreatorWithLookup{}
	mgr := NewNotificationManager(bird.NewClientForTest(srv.URL),
		services.NewAppointmentService(&mockApptRepoNotif{}, nil),
		&config.Config{BirdTeamFallback: "team-fallback"})
	mgr.SetWaitingListDeps(nil, repo, &mockVirtualEnqueuer{})

	mgr.escalateToAgent(&PendingNotification{
		Phone: "+573001112233", AppointmentID: "APT001", Type: "confirmation", ConversationID: "conv-1",
	}, escalationNoResponse)

	repo.mu.Lock()
	defer repo.mu.Unlock()
	if repo.findCalls != 0 || repo.createCalls != 0 {
		t.Errorf("Bird no busca ni crea sesión: find=%d create=%d", repo.findCalls, repo.createCalls)
	}
}

// Una escalación de Néctar que FALLA no puede dejar al paciente en una sesión escalada sin agente:
// el worker ignoraría sus mensajes hasta que el barrido de vencidas la liberara (hasta 24 h). La
// sesión solo se persiste si la escalación tuvo éxito; la activa que ya tenía queda como estaba.
func TestNectarNotifEscalation_FailedEscalationLeavesTheSessionAlone(t *testing.T) {
	for name, escalate := range map[string]func(*NotificationManager){
		"confirmación": func(m *NotificationManager) {
			m.escalateToAgent(&PendingNotification{
				Phone: "nectar:42", AppointmentID: "APT001", Type: "confirmation", ConversationID: "42",
			}, escalationNoResponse)
		},
		"texto libre": func(m *NotificationManager) {
			m.escalateNotifToAgent(&PendingNotification{
				Phone: "nectar:42", AppointmentID: "APT001", Type: "confirmation",
			}, "42")
		},
	} {
		for _, existing := range []*session.Session{nil, {ID: "sess-activa", PhoneNumber: "nectar:42", Status: session.StatusActive}} {
			t.Run(fmt.Sprintf("%s/activa=%v", name, existing != nil), func(t *testing.T) {
				repo := &mockSessionCreatorWithLookup{existing: existing}
				mgr, bridge, tracker, rec := confirmationEscalationManagerWithBridge(t, repo)
				bridge.fail = errors.New("néctar caído")

				escalate(mgr)

				repo.mu.Lock()
				defer repo.mu.Unlock()
				if repo.createCalls != 0 || len(repo.statusCalls) != 0 || repo.batchSession != "" || len(repo.renewed) != 0 {
					t.Errorf("sin escalación no se toca la sesión: create=%d status=%v batch=%q renew=%v",
						repo.createCalls, repo.statusCalls, repo.batchSession, repo.renewed)
				}
				rec.mu.Lock()
				rows := rec.created
				rec.mu.Unlock()
				tracker.mu.Lock()
				evs := append([]string{}, tracker.events...)
				tracker.mu.Unlock()
				if rows != 0 || slices.Contains(evs, "escalated_to_agent") {
					t.Errorf("una escalación fallida no se registra: filas=%d eventos=%v", rows, evs)
				}
			})
		}
	}
}

// Al reutilizar la sesión activa en Néctar se renueva su vencimiento: si no, el barrido de
// vencidas podría cerrarla antes de que vuelva el retorno del agente.
func TestNectarNotifEscalation_ReusedSessionRenewsExpiry(t *testing.T) {
	repo := &mockSessionCreatorWithLookup{existing: &session.Session{
		ID: "sess-activa", PhoneNumber: "nectar:42", Status: session.StatusActive,
		ExpiresAt: time.Now().Add(time.Minute),
	}}
	mgr, _, _ := confirmationEscalationManager(t, repo)

	mgr.escalateToAgent(&PendingNotification{
		Phone: "nectar:42", AppointmentID: "APT001", Type: "confirmation", ConversationID: "42",
	}, escalationNoResponse)

	repo.mu.Lock()
	defer repo.mu.Unlock()
	until, ok := repo.renewed["sess-activa"]
	if !ok || time.Until(until) < 23*time.Hour {
		t.Errorf("el vencimiento se renueva ~24 h: renovada=%v hasta=%v", ok, until)
	}
}

// Bird no cambia: la sesión reutilizada conserva su vencimiento.
func TestEscalateNotifToAgent_BirdReusedSessionKeepsExpiry(t *testing.T) {
	srv := escalationBirdServer()
	defer srv.Close()
	repo := &mockSessionCreatorWithLookup{existing: &session.Session{
		ID: "sess-existente", PhoneNumber: "+573001234567", Status: session.StatusActive,
	}}
	mgr := NewNotificationManager(bird.NewClientForTest(srv.URL),
		services.NewAppointmentService(&mockApptRepoNotif{}, nil),
		&config.Config{BirdTeamFallback: "team-fallback"})
	mgr.SetWaitingListDeps(nil, repo, &mockVirtualEnqueuer{})

	mgr.escalateNotifToAgent(&PendingNotification{
		Phone: "+573001234567", AppointmentID: "APT001", Type: "confirmation",
	}, "conv-1")

	repo.mu.Lock()
	defer repo.mu.Unlock()
	if len(repo.renewed) != 0 {
		t.Errorf("Bird no renueva el vencimiento: %v", repo.renewed)
	}
}
