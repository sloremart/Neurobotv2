package notifications

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/config"
	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/services"
	"github.com/neuro-bot/neuro-bot/internal/session"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
)

// noProcedureNectarManager arma un manager con el puente de Néctar, una cita sin procedimiento y
// un servidor de Bird que falla la prueba si recibe cualquier llamada.
func noProcedureNectarManager(t *testing.T, bridge *escalationBridge) (*NotificationManager, *mockSessionCreatorWithLookup) {
	t.Helper()
	registerNotifPendingMenu(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("una conversación de Néctar no debe llamar a Bird: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(500)
	}))
	t.Cleanup(srv.Close)
	client := bird.NewClientForTest(srv.URL)
	client.SetNectar(bridge)
	repo := &mockApptRepoNotif{
		findByIDFn: func(context.Context, string) (*domain.Appointment, error) {
			appt := sampleAppt()
			appt.Procedures = nil
			appt.PatientName = "Ana Pérez"
			return &appt, nil
		},
	}
	mgr := NewNotificationManager(client, services.NewAppointmentService(repo, nil), &config.Config{
		BirdTeamFallback: "team-fallback", ProtocolEscaladoVersion: config.ProtocolEscaladoV1,
	})
	sessions := &mockSessionCreatorWithLookup{}
	mgr.SetWaitingListDeps(nil, sessions, &mockVirtualEnqueuer{})
	return mgr, sessions
}

// En Néctar, «Reprogramar» sobre una cita sin procedimiento escala al agente con su motivo y deja
// la sesión escalada en NOTIF_PENDING para el retorno. Antes iba un PATCH a Bird con el id de
// Néctar y el paciente quedaba esperando a un agente que nunca llegaba.
func TestSelfReschedule_NoProcedureNectarEscalates(t *testing.T) {
	bridge := &escalationBridge{}
	mgr, sessions := noProcedureNectarManager(t, bridge)

	mgr.startSelfReschedule(nectarPhone, &PendingNotification{
		Phone: nectarPhone, AppointmentID: "APT001", Type: "cancellation", ConversationID: "777",
	}, true)

	esc := bridge.only(t)
	if esc.ReasonDetail != sm.EscalationReasonRescheduleNoProcedure {
		t.Errorf("ReasonDetail = %q", esc.ReasonDetail)
	}
	if !strings.Contains(esc.Summary, "Cita ID: APT001") || !strings.Contains(esc.Summary, "Ana Pérez") {
		t.Errorf("el resumen debe identificar la cita y el paciente: %q", esc.Summary)
	}
	values, exits := descriptorOptions(t, esc.Input)
	// Las propias del motivo van primero; detrás, las universales (bot_reanudar…).
	if len(values) < 2 || values[0] != "reprogramada_manual" || exits[0] != "cerrar" || values[1] != "understood" {
		t.Errorf("opciones = %v (salidas %v)", values, exits)
	}

	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	if sessions.created == nil || sessions.created.Status != session.StatusEscalated ||
		sessions.created.CurrentState != sm.StateNotifPending {
		t.Fatalf("esperaba una sesión ESCALADA en NOTIF_PENDING, got %+v", sessions.created)
	}
	if got := sessions.batchCtx[sm.CtxNotifEscalationDetail]; got != sm.EscalationReasonRescheduleNoProcedure {
		t.Errorf("detalle en la sesión = %q", got)
	}
	if got := sessions.batchCtx["notif_type"]; got != "cancellation" {
		t.Errorf("notif_type = %q", got)
	}
}

// Si Néctar rechaza la escalación no queda una sesión ESCALADA sin agente (el paciente se quedaría
// sin bot hasta el barrido de vencidas).
func TestSelfReschedule_NoProcedureNectarEscalationFailsLeavesNoSession(t *testing.T) {
	bridge := &escalationBridge{fail: errors.New("boom")}
	mgr, sessions := noProcedureNectarManager(t, bridge)

	mgr.startSelfReschedule(nectarPhone, &PendingNotification{
		Phone: nectarPhone, AppointmentID: "APT001", Type: "cancellation", ConversationID: "777",
	}, true)

	bridge.only(t)
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	if sessions.createCalls != 0 {
		t.Errorf("no debe crearse sesión tras una escalación fallida: %d", sessions.createCalls)
	}
}
