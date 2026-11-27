package handlers

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/nectar"
	"github.com/neuro-bot/neuro-bot/internal/services"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
	"github.com/neuro-bot/neuro-bot/internal/testutil"
	"github.com/neuro-bot/neuro-bot/internal/utils"
)

// NOTIF_RESCHEDULE_FALLBACK: no hubo horarios para reprogramar y la cita original sigue vigente.
// «Reprogramar» (que el paso ya mostraba sin aceptarlo) escala a un agente para que reprograme a
// mano, sin tocar la cita; confirmar y cancelar siguen igual.

func TestNotifRescheduleFallback_RescheduleEscalatesToAnAgent(t *testing.T) {
	repo := newNotifApptRepo()
	m := sm.NewMachine()
	RegisterAppointmentHandlers(m, services.NewAppointmentService(repo, nil), nil, nil, nil, nil)
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
			RegisterAppointmentHandlers(m, services.NewAppointmentService(repo, nil), nil, nil, nil, nil)
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

// Al escalar «Reprogramar» sin horarios, los datos del caso traen la cita ORIGINAL (la de
// reschedule_appt_id) venga la reprogramación de una notificación o de «Mis citas».
func TestNotifRescheduleFallback_CaseFactsShowTheOriginalAppointment(t *testing.T) {
	day := time.Date(2026, 11, 3, 0, 0, 0, 0, time.Local)
	other := testutil.SampleAppointment(day.AddDate(0, 0, 1))
	other.ID = "APT-OTRA"
	original := testutil.SampleAppointment(day)
	original.ID = "APT-ORIGINAL"
	original.TimeSlot = "202611030730"
	apptsJSON, _ := json.Marshal([]domain.Appointment{other, original})

	base := map[string]string{
		sm.CtxEscalationReason: sm.EscalationReasonRescheduleNoSlots,
		"reschedule_appt_id":   "APT-ORIGINAL", "cups_code": "890271", "cups_name": "Electromiografia",
	}
	cases := map[string]map[string]string{
		// startConfirmRescheduleSession / NOTIF_PENDING: la cita viene en las claves de la notificación.
		"notificación": {
			"notif_appt_date": utils.FormatFriendlyDate(day), "notif_appt_time": "7:30 AM",
			"notif_cups_name": "Electromiografia",
		},
		// «Mis citas»: la cita está en la lista que vio el paciente; la seleccionada puede ser otra.
		"mis citas": {"appointments_json": string(apptsJSON), "selected_appointment_id": "APT-ORIGINAL"},
		// startSelfReschedule (cancelación de la clínica): ni lista ni claves de la notificación; la
		// cita viene en las claves de visualización de RescheduleContext.
		"reprogramación propia": {
			"reschedule_appt_date": utils.FormatFriendlyDate(day), "reschedule_appt_time": "7:30 AM",
			"reschedule_appt_cups_name": "Electromiografia",
		},
		// Varias citas de una notificación: la notificada es otra; manda la que se reprogramaba.
		"notificación con varias citas": {
			"appointments_json": string(apptsJSON), "notif_appointment_id": "APT-OTRA",
			"notif_appt_date": "otro día", "notif_appt_time": "9:00 AM", "notif_cups_name": "Otra",
		},
	}
	want := map[string]string{
		nectar.FactAppointmentID.Key:   "APT-ORIGINAL",
		nectar.FactAppointmentDate.Key: utils.FormatFriendlyDate(day),
		nectar.FactAppointmentTime.Key: "7:30 AM",
		nectar.FactProcedure.Key:       "Electromiografia",
	}
	for name, extra := range cases {
		t.Run(name, func(t *testing.T) {
			sess := testSess(sm.StateEscalateToAgent)
			for _, kv := range []map[string]string{base, extra} {
				for k, v := range kv {
					sess.Context[k] = v
				}
			}
			_, groups := summaryForAgent(sess, "890271", "Call Center")
			got := map[string]string{}
			for _, g := range groups {
				if g.Key != "cita" {
					continue
				}
				for _, f := range g.Fields {
					got[f.Key] = f.Value
				}
			}
			for k, v := range want {
				if got[k] != v {
					t.Errorf("%s = %q, quería %q (cita: %v)", k, got[k], v, got)
				}
			}
		})
	}
}
