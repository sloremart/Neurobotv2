package worker

import (
	"context"
	"slices"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/session"
	"github.com/neuro-bot/neuro-bot/internal/statemachine"
)

// capPool arma una sesión escalada con el tope de escalaciones lleno y registra qué claves borra
// cada SaveState.
func capPool(t *testing.T) (*MessageWorkerPool, *mockSessionMgmt, *session.Session, *[]string) {
	t.Helper()
	pool, sm, _, sess := escalatedPool(t)
	sess.Context["pre_escalation_state"] = "ASK_DOCUMENT"
	sess.Context[statemachine.CtxEscalationCount] = "3"
	sess.Context[statemachine.CtxEscalationCapped] = "1"
	resume := func(_ context.Context, s *session.Session, target string) error {
		s.Status = session.StatusActive
		s.CurrentState = target
		return nil
	}
	sm.resumeFn = resume
	sm.resumeNoShowFn = resume
	var cleared []string
	sm.saveFn = func(_ context.Context, s *session.Session, _ string, _ map[string]string, clearCtx []string) error {
		cleared = append(cleared, clearCtx...)
		for _, k := range clearCtx {
			delete(s.Context, k)
		}
		return nil
	}
	return pool, sm, sess, &cleared
}

func clearsCap(cleared []string) bool {
	return slices.Contains(cleared, statemachine.CtxEscalationCount) &&
		slices.Contains(cleared, statemachine.CtxEscalationCapped)
}

// Un agente que atendió y devolvió la sesión (Bird `/bot resume`, aviso de devuelta de Néctar,
// `/bot cups`) reinicia el tope: fue una persona, no un lazo del bot.
func TestAgentReturn_ResetsEscalationCap(t *testing.T) {
	for name, cmd := range map[string]AgentCommand{
		"resume": {Action: "resume", State: "ASK_DOCUMENT", Phone: "+573001234567"},
		"cups":   {Action: "cups", Data: "883141", Phone: "+573001234567"},
	} {
		t.Run(name, func(t *testing.T) {
			pool, _, _, cleared := capPool(t)
			pool.processAgentCommand(context.Background(), cmd)
			if !clearsCap(*cleared) {
				t.Errorf("el retorno del agente debe borrar el tope en la BD, borró %v", *cleared)
			}
		})
	}
}

// El no_show NO reinicia el tope: ahí nace el bucle del incidente 11/12-ago.
func TestAgentNoShow_KeepsEscalationCap(t *testing.T) {
	pool, _, sess, cleared := capPool(t)
	pool.processAgentCommand(context.Background(), AgentCommand{Action: "no_show", Phone: "+573001234567"})
	if clearsCap(*cleared) || sess.Context[statemachine.CtxEscalationCount] != "3" {
		t.Errorf("el no_show no toca el tope: borró %v, contador %q", *cleared, sess.Context[statemachine.CtxEscalationCount])
	}
}
