package handlers

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/session"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
)

// «Reprogramar» sin horarios escala desde NOTIF_RESCHEDULE_FALLBACK. Si la escalación NO llega al
// agente (tope, fuera de horario, sin asesores…), el paciente vuelve al menú y el trámite de
// reprogramación no puede seguir vivo: si luego agenda otra cosa, createAppointmentHandler
// cancelaría la cita original. Si llega al agente, se conserva: el agente la necesita.

func rescheduleEscalationSess() *session.Session {
	sess := testSess(sm.StateEscalateToAgent)
	sess.ConversationID = "conv-test"
	sess.Context[sm.CtxPreAutoState] = sm.StateNotifRescheduleFallback
	sess.Context[sm.CtxEscalationReason] = sm.EscalationReasonRescheduleNoSlots
	for _, k := range rescheduleTramiteKeys {
		sess.Context[k] = "viejo"
	}
	sess.Context["reschedule_appt_id"] = "APT-ORIGINAL"
	sess.Context["reschedule_skip_cancel"] = "0"
	return sess
}

func TestEscalateFromRescheduleFallback_DivertedExitDropsTheReschedule(t *testing.T) {
	cases := map[string]func(t *testing.T) (*sm.Machine, *session.Session){
		"fuera de horario": func(t *testing.T) (*sm.Machine, *session.Session) {
			old := nowFunc
			nowFunc = func() time.Time { return time.Date(2026, 8, 12, 4, 0, 0, 0, time.UTC) }
			t.Cleanup(func() { nowFunc = old })
			cfg := testEscalationConfig()
			cfg.TestingAlwaysOpen = false
			m := sm.NewMachine()
			RegisterEscalationHandlers(m, bird.NewClientForTest(escalationTestServer(t).URL), cfg, nil)
			return m, rescheduleEscalationSess()
		},
		"tope de la sesión": func(t *testing.T) (*sm.Machine, *session.Session) {
			m := sm.NewMachine()
			RegisterEscalationHandlers(m, bird.NewClientForTest(escalationTestServer(t).URL), testEscalationConfig(), nil)
			sess := rescheduleEscalationSess()
			sess.Context["escalation_count"] = "3"
			return m, sess
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			m, sess := setup(t)
			result, err := m.Process(context.Background(), sess, textM(""))
			if err != nil {
				t.Fatal(err)
			}
			if result.NextState == sm.StateEscalated {
				t.Fatal("el caso debía desviarse, no escalar")
			}
			for _, k := range rescheduleTramiteKeys {
				if !slices.Contains(result.ClearCtx, k) {
					t.Errorf("%s sobrevive a la escalación desviada; ClearCtx=%v", k, result.ClearCtx)
				}
			}
		})
	}
}

func TestEscalateFromRescheduleFallback_RealEscalationKeepsTheReschedule(t *testing.T) {
	m := sm.NewMachine()
	RegisterEscalationHandlers(m, bird.NewClientForTest(escalationTestServer(t).URL), testEscalationConfig(), nil)
	sess := rescheduleEscalationSess()

	result, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateEscalated {
		t.Fatalf("next = %s, debía escalar", result.NextState)
	}
	if slices.Contains(result.ClearCtx, "reschedule_appt_id") || sess.GetContext("reschedule_appt_id") != "APT-ORIGINAL" {
		t.Errorf("la escalación real conserva la cita a reprogramar; ClearCtx=%v", result.ClearCtx)
	}
}

// Desde otro paso, una escalación desviada no toca una reprogramación en curso.
func TestEscalateFromOtherStep_DivertedExitKeepsTheReschedule(t *testing.T) {
	m := sm.NewMachine()
	RegisterEscalationHandlers(m, bird.NewClientForTest(escalationTestServer(t).URL), testEscalationConfig(), nil)
	sess := rescheduleEscalationSess()
	sess.Context[sm.CtxPreAutoState] = sm.StateShowSlots
	sess.Context["escalation_count"] = "3"

	result, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(result.ClearCtx, "reschedule_appt_id") {
		t.Errorf("solo la escalación de NOTIF_RESCHEDULE_FALLBACK suelta el trámite; ClearCtx=%v", result.ClearCtx)
	}
}
