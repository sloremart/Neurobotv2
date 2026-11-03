package handlers

import (
	"context"
	"maps"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
)

// escalateTurn corre UNA escalación sobre el contexto guardado (como si la sesión se recargara de
// la BD en cada mensaje) y le aplica lo que el turno persiste: primero los borrados, luego lo escrito.
func escalateTurn(t *testing.T, m *sm.Machine, stored map[string]string) *sm.StateResult {
	t.Helper()
	sess := testSess(sm.StateEscalateToAgent)
	sess.ConversationID = "conv-test"
	sess.Context = maps.Clone(stored)
	res, err := m.Process(context.Background(), sess, textM("quiero un asesor"))
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range res.ClearCtx {
		delete(stored, k)
	}
	maps.Copy(stored, res.UpdateCtx)
	return res
}

func hasEvent(res *sm.StateResult, typ string) bool {
	for _, ev := range res.Events {
		if ev.Type == typ {
			return true
		}
	}
	return false
}

// B-06: sin asesores, cada desvío suma al tope de escalaciones de la sesión. Un contestador que
// re-escala sin fin recibe a lo sumo escalationSessionCap avisos de «no hay asesores» y después el
// mismo corte de siempre del tope.
func TestEscalationDiversions_AreBounded(t *testing.T) {
	cfg := testEscalationConfig()
	m := sm.NewMachine()
	RegisterEscalationHandlers(m, bird.NewClientForTest(noAgentsServer(t).URL), cfg, nil)

	stored := map[string]string{}
	noAgents, capped := 0, 0
	for i := 1; i <= 50; i++ {
		res := escalateTurn(t, m, stored)
		switch {
		case hasEvent(res, "escalation_no_agents"):
			noAgents++
		case hasEvent(res, "escalation_cap_reached"):
			capped++
			if res.NextState != sm.StateFallbackMenu {
				t.Fatalf("turno %d: el tope devuelve FALLBACK_MENU como hoy, got %s", i, res.NextState)
			}
		default:
			t.Fatalf("turno %d: salida inesperada %+v", i, res.Events)
		}
	}
	if noAgents != escalationSessionCap {
		t.Errorf("avisos de «no hay asesores» = %d, quería %d", noAgents, escalationSessionCap)
	}
	if capped != 50-escalationSessionCap {
		t.Errorf("cortes del tope = %d, quería %d", capped, 50-escalationSessionCap)
	}
}

// Un paciente real que de noche pide un asesor una o dos veces sigue recibiendo el aviso de
// «no hay asesores» con el menú, como hoy.
func TestEscalationDiversions_PatientAskingTwiceStillGetsNoAgentsNotice(t *testing.T) {
	cfg := testEscalationConfig()
	m := sm.NewMachine()
	RegisterEscalationHandlers(m, bird.NewClientForTest(noAgentsServer(t).URL), cfg, nil)

	stored := map[string]string{}
	for i := 1; i <= 2; i++ {
		res := escalateTurn(t, m, stored)
		if res.NextState != sm.StateMainMenu || !hasEvent(res, "escalation_no_agents") {
			t.Fatalf("pedido %d: esperaba MAIN_MENU con escalation_no_agents, got %s %+v", i, res.NextState, res.Events)
		}
		if len(res.Messages) != 2 {
			t.Fatalf("pedido %d: esperaba aviso + menú, got %d mensajes", i, len(res.Messages))
		}
		if txt, ok := res.Messages[0].(*sm.TextMessage); !ok || txt.Text != noAgentsText(cfg) {
			t.Errorf("pedido %d: el aviso es el de siempre, got %+v", i, res.Messages[0])
		}
		if _, ok := res.Messages[1].(*sm.ListMessage); !ok {
			t.Errorf("pedido %d: el menú principal acompaña el aviso, got %T", i, res.Messages[1])
		}
	}
}

// El contador de una escalación entregada también tiene que sobrevivir al turno: escrito solo en
// memoria, la sesión recargada de la BD lo perdía y el tope nunca saltaba entre mensajes.
func TestEscalateHandler_CountPersistsAcrossTurns(t *testing.T) {
	m := sm.NewMachine()
	RegisterEscalationHandlers(m, bird.NewClientForTest(escalationTestServer(t).URL), testEscalationConfig(), nil)

	stored := map[string]string{}
	res := escalateTurn(t, m, stored)
	if res.NextState != sm.StateEscalated {
		t.Fatalf("esperaba ESCALATED, got %s", res.NextState)
	}
	if got := stored["escalation_count"]; got != "1" {
		t.Errorf("escalation_count persistido = %q, quería \"1\"", got)
	}
}
