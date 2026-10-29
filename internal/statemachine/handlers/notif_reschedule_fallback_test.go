package handlers

import (
	"context"
	"slices"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/services"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
)

// NOTIF_RESCHEDULE_FALLBACK: no hubo horarios para reprogramar y la cita original sigue vigente.
// «Reprogramar» (que el paso ya mostraba sin aceptarlo) escala a un agente para que reprograme a
// mano, sin tocar la cita; confirmar y cancelar siguen igual.

func TestNotifRescheduleFallback_RescheduleEscalatesToAnAgent(t *testing.T) {
	repo := newNotifApptRepo()
	m := sm.NewMachine()
	RegisterAppointmentHandlers(m, services.NewAppointmentService(repo, nil), nil, nil, nil)
	sess := testSess(sm.StateNotifRescheduleFallback)
	sess.Context["reschedule_appt_id"] = "APT-NOTIF"
	sess.Context["cups_name"] = "Electromiografia"

	result, err := m.Process(context.Background(), sess, postbackM("reschedule"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateEscalateToAgent {
		t.Fatalf("next = %s, quería %s", result.NextState, sm.StateEscalateToAgent)
	}
	if got := result.UpdateCtx[sm.CtxEscalationReason]; got != sm.EscalationReasonRescheduleNoSlots {
		t.Errorf("escalation_reason = %q, quería %q", got, sm.EscalationReasonRescheduleNoSlots)
	}
	// La escalación retoma en el paso de la fallback (ESCALATE_TO_AGENT la lee de aquí).
	if got := sess.GetContext(sm.CtxPreAutoState); got != sm.StateNotifRescheduleFallback {
		t.Errorf("estado previo = %q", got)
	}
	if len(repo.confirmed) != 0 || len(repo.cancelled) != 0 {
		t.Errorf("la cita original no se toca: confirmadas=%v canceladas=%v", repo.confirmed, repo.cancelled)
	}
	if txt := firstText(t, result); txt == "" {
		t.Error("el paciente debe recibir el aviso de la escalación")
	}
}

func TestNotifRescheduleFallback_ConfirmAndCancelUnchanged(t *testing.T) {
	for action, want := range map[string]string{"confirm": "confirmed", "cancel": "cancelled"} {
		t.Run(action, func(t *testing.T) {
			repo := newNotifApptRepo()
			m := sm.NewMachine()
			RegisterAppointmentHandlers(m, services.NewAppointmentService(repo, nil), nil, nil, nil)
			sess := testSess(sm.StateNotifRescheduleFallback)
			sess.Context["reschedule_appt_id"] = "APT-NOTIF"

			result, err := m.Process(context.Background(), sess, postbackM(action))
			if err != nil {
				t.Fatal(err)
			}
			got := repo.confirmed
			if want == "cancelled" {
				got = repo.cancelled
			}
			if !slices.Equal(got, []string{"APT-NOTIF", "APT-SAME-DAY"}) || result.NextState != sm.StateTerminated {
				t.Errorf("%s: ids=%v next=%s", want, got, result.NextState)
			}
		})
	}
}

// El agente ve confirmar y cancelar del paso, más reanudar y cerrar; no «Reprogramar».
func TestNotifRescheduleFallback_AgentMenu(t *testing.T) {
	returnMenuMachine()
	var values []string
	for _, o := range sm.ReturnOptionsFor(testSess(sm.StateNotifRescheduleFallback), sm.StateNotifRescheduleFallback) {
		values = append(values, o.Value)
	}
	for _, v := range []string{"confirm", "cancel", sm.ReturnResume, sm.ReturnClose} {
		if !slices.Contains(values, v) {
			t.Errorf("falta %s: %v", v, values)
		}
	}
	if slices.Contains(values, "reschedule") {
		t.Errorf("el agente no debe tener «Reprogramar» (lo escalaría de vuelta): %v", values)
	}
}
