package handlers

import (
	"context"
	"maps"
	"slices"
	"strconv"
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
	if got := stored[sm.CtxEscalationCount]; got != "1" {
		t.Errorf("escalation_count persistido = %q, quería \"1\"", got)
	}
}

// El kill switch (BOT_ENABLED=false) manda todo a los asesores: queda EXENTO del tope, ni suma ni
// corta. Sin asesores, cada mensaje recibe el aviso de siempre; cuando vuelven, se escala.
func TestEscalationCap_BotDisabledIsExempt(t *testing.T) {
	cfg := testEscalationConfig()
	noAgents := sm.NewMachine()
	RegisterEscalationHandlers(noAgents, bird.NewClientForTest(noAgentsServer(t).URL), cfg, nil)

	stored := map[string]string{}
	killSwitch := func() {
		// Lo que el worker escribe en memoria antes de cada mensaje con el bot apagado.
		stored[sm.CtxPreAutoState] = sm.BotDisabledStep
		stored[sm.CtxEscalationReason] = sm.EscalationReasonBotDisabled
	}
	for i := 1; i <= escalationSessionCap+2; i++ {
		killSwitch()
		res := escalateTurn(t, noAgents, stored)
		if !hasEvent(res, "escalation_no_agents") {
			t.Fatalf("mensaje %d con el bot apagado: esperaba el aviso de «no hay asesores», got %+v", i, res.Events)
		}
	}
	if got := stored[sm.CtxEscalationCount]; got != "" {
		t.Errorf("el kill switch no suma al tope: escalation_count = %q", got)
	}

	withAgents := sm.NewMachine()
	RegisterEscalationHandlers(withAgents, bird.NewClientForTest(escalationTestServer(t).URL), cfg, nil)
	stored[sm.CtxEscalationCount] = strconv.Itoa(escalationSessionCap) // ni con el contador lleno corta
	killSwitch()
	if res := escalateTurn(t, withAgents, stored); res.NextState != sm.StateEscalated {
		t.Fatalf("con asesores el kill switch escala, got %s %+v", res.NextState, res.Events)
	}
}

// Un agente que atendió y devolvió la sesión al bot reinicia el contador (worker,
// clearEscalationCap): escalaciones entregadas con su devolución en medio no llegan al tope.
func TestEscalationCap_AgentReturnBetweenEscalationsNeverCaps(t *testing.T) {
	m := sm.NewMachine()
	RegisterEscalationHandlers(m, bird.NewClientForTest(escalationTestServer(t).URL), testEscalationConfig(), nil)

	stored := map[string]string{}
	for i := 1; i <= escalationSessionCap+2; i++ {
		res := escalateTurn(t, m, stored)
		if res.NextState != sm.StateEscalated {
			t.Fatalf("escalación %d: esperaba ESCALATED, got %s %+v", i, res.NextState, res.Events)
		}
		for _, k := range sm.EscalationCapKeys() { // el agente la devuelve
			delete(stored, k)
		}
	}
}

// Tras el corte por tope el bot deja de responder: en FALLBACK_MENU solo atiende los botones del
// corte y la palabra de reinicio; el resto se ignora en silencio (con un evento).
func TestEscalationCap_SilentAfterCap(t *testing.T) {
	m := sm.NewMachine()
	sm.RegisterInterceptors(m)
	RegisterEscalationHandlers(m, bird.NewClientForTest(noAgentsServer(t).URL), testEscalationConfig(), nil)
	RegisterPostActionHandlers(m, nil)

	stored := map[string]string{sm.CtxEscalationCount: strconv.Itoa(escalationSessionCap)}
	res := escalateTurn(t, m, stored)
	if !hasEvent(res, "escalation_cap_reached") || res.NextState != sm.StateFallbackMenu {
		t.Fatalf("esperaba el corte del tope, got %s %+v", res.NextState, res.Events)
	}
	if stored[sm.CtxEscalationCapped] == "" {
		t.Fatal("la marca de sesión cortada debe persistirse en el contexto")
	}

	turn := func(msg bird.InboundMessage) *sm.StateResult {
		t.Helper()
		sess := testSess(sm.StateFallbackMenu)
		sess.Context = maps.Clone(stored)
		r, err := m.Process(context.Background(), sess, msg)
		if err != nil {
			t.Fatal(err)
		}
		for _, k := range r.ClearCtx {
			delete(stored, k)
		}
		maps.Copy(stored, r.UpdateCtx)
		return r
	}
	sent := 0
	for i := 0; i < 50; i++ {
		r := turn(textM("Gracias por escribir, pronto te respondemos"))
		sent += len(r.Messages)
		if r.NextState != sm.StateFallbackMenu || !hasEvent(r, "escalation_cap_input_ignored") {
			t.Fatalf("entrada %d: esperaba quedarse en FALLBACK_MENU con el evento, got %s %+v", i, r.NextState, r.Events)
		}
	}
	for _, msg := range []bird.InboundMessage{textM("asesor"), textM("1"), postbackM("otra_cosa")} {
		sent += len(turn(msg).Messages)
	}
	if sent != 0 {
		t.Errorf("tras el corte no se envía nada, got %d mensajes", sent)
	}

	if r := turn(postbackM("action:end")); r.NextState != sm.StateTerminated || len(r.Messages) == 0 {
		t.Errorf("el botón «Terminar chat» sigue funcionando, got %s con %d mensajes", r.NextState, len(r.Messages))
	}
}

// La palabra de reinicio sale del silencio como hoy: vuelve al saludo y borra todo el contexto.
func TestEscalationCap_MenuKeywordStillRestarts(t *testing.T) {
	m := sm.NewMachine()
	sm.RegisterInterceptors(m)
	RegisterPostActionHandlers(m, nil)

	sess := testSess(sm.StateFallbackMenu)
	sess.SetContext(sm.CtxEscalationCapped, "1")
	res, err := m.Process(context.Background(), sess, textM("menú"))
	if err != nil {
		t.Fatal(err)
	}
	if res.NextState != sm.StateGreeting || !slices.Contains(res.ClearCtx, "__all__") {
		t.Errorf("«menú» reinicia como hoy, got %s %v", res.NextState, res.ClearCtx)
	}
}

// FALLBACK_MENU por otros caminos (sin la marca del tope) sigue contestando como hoy.
func TestFallbackMenu_WithoutCapMarkStillReplies(t *testing.T) {
	m := sm.NewMachine()
	sm.RegisterInterceptors(m)
	RegisterPostActionHandlers(m, nil)

	res, err := m.Process(context.Background(), testSess(sm.StateFallbackMenu), textM("hola?"))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Messages) != 1 || hasEvent(res, "escalation_cap_input_ignored") {
		t.Fatalf("sin la marca se re-muestran los botones, got %+v %+v", res.Messages, res.Events)
	}
	if bm, ok := res.Messages[0].(*sm.ButtonMessage); !ok || bm.Text != "Por favor selecciona una opción:" {
		t.Errorf("el texto de hoy, got %+v", res.Messages[0])
	}
}
