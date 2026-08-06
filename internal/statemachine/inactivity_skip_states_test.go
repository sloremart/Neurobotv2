package statemachine

import (
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/session"
)

// El paquete session no puede importar statemachine (ciclo), así que su set de estados sin
// progreso usa literales. Este test los mantiene sincronizados con las constantes reales:
// si un estado se renombra, falla aquí en vez de dejar el gate apuntando a un nombre muerto.
func TestInactivitySkipStates_MatchStateMachine(t *testing.T) {
	expected := map[string]bool{
		StateCheckBusinessHours: true,
		StateGreeting:           true,
		StateMainMenu:           true,
		StateOutOfHoursMenu:     true,
		StateOutOfHours:         true,
		StateFallbackMenu:       true,
		StateShowHelp:           true,
	}
	got := session.InactivityReminderSkipStates()
	for st := range expected {
		if !got[st] {
			t.Errorf("falta %q en session.inactivityReminderSkipStates", st)
		}
	}
	for st := range got {
		if !expected[st] {
			t.Errorf("estado %q en el skip-set no corresponde a ninguna constante esperada", st)
		}
	}
}
