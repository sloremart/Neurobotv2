package statemachine

import (
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/session"
)

// El paquete session no puede importar statemachine (ciclo), así que repite el literal de la
// clave de escalación encolada; este test los mantiene sincronizados.
func TestEscalationQueuedForNextDayKey_MatchStateMachine(t *testing.T) {
	if session.EscalationQueuedForNextDayKey != CtxEscalationNextDay {
		t.Fatalf("session.EscalationQueuedForNextDayKey = %q, statemachine.CtxEscalationNextDay = %q: deben ser iguales",
			session.EscalationQueuedForNextDayKey, CtxEscalationNextDay)
	}
}
