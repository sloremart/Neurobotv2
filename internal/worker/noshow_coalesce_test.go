package worker

import (
	"context"
	"strings"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/session"
	"github.com/neuro-bot/neuro-bot/internal/statemachine"
)

// El equipo de hdg-bot senalo el patron "acuse por SendText directo + sendAndSave justo despues":
// dos mensajes cobrados donde el coalescer haria uno. Listaron los cinco sitios de comandos de
// agente (pool.go 863/883/924/1066/1151), pero MEDIDO en produccion esos cinco corren 0 veces:
// ningun agente ha usado /bot en 30 dias (flow_events escalacion/agent_resumed = 0; los 531
// comandos de la semana son no_show, que encola el propio bot).
//
// El mismo patron SI esta vivo en handleAgentNoShow, y ahi corre 531 veces / 7d. Cada no-show
// cobra el aviso "No pudimos conectarte..." aparte del menu que sale medio segundo despues.
func TestNoShow_NoticeAndPromptAreOneChargedMessage(t *testing.T) {
	pool, sm, sender, sess := escalatedPool(t)
	sess.Context["pre_escalation_state"] = "MAIN_MENU"
	sm.resumeNoShowFn = func(_ context.Context, s *session.Session, target string) error {
		s.Status = session.StatusActive
		s.CurrentState = target
		return nil
	}
	// El __resume__ virtual entra al cortacircuitos anti-bucle de escalateHandler, que devuelve el
	// menu principal: exactamente lo que ocurre en produccion tras un no-show.
	pool.SetDependencies(sm, sender, &mockMessageProcessor{
		processFn: func(_ context.Context, _ *session.Session, _ bird.InboundMessage) (*statemachine.StateResult, error) {
			r := statemachine.NewResult(statemachine.StateMainMenu)
			r.Messages = append(r.Messages, &statemachine.ListMessage{
				Body:  "¿En qué puedo ayudarte hoy?",
				Title: "Menú",
			})
			return r, nil
		},
	})

	pool.processAgentCommand(context.Background(), AgentCommand{Action: "no_show", Phone: "+573001234567"})

	sender.mu.Lock()
	defer sender.mu.Unlock()
	for i, m := range sender.sent {
		t.Logf("envio %d [%s]: %s", i+1, m.msgType, m.text)
	}
	if len(sender.sent) != 1 {
		t.Fatalf("un no-show debe costar UN envio (aviso fundido con el menu), costo %d — "+
			"a 531 no-shows/semana eso es un mensaje cobrado de mas por cada uno", len(sender.sent))
	}
	// Y el paciente debe seguir leyendo LO MISMO: fusionar no puede perder ninguno de los dos textos.
	if !strings.Contains(sender.sent[0].text, "No pudimos conectarte") {
		t.Error("se perdio el aviso de no-show al fusionar")
	}
	if !strings.Contains(sender.sent[0].text, "¿En qué puedo ayudarte hoy?") {
		t.Errorf("se perdio el prompt del turno al fusionar: %q", sender.sent[0].text)
	}
	if sender.sent[0].msgType != "list" {
		t.Errorf("el envio fusionado debe seguir siendo la lista interactiva, fue %q", sender.sent[0].msgType)
	}
}

// Borde que el documento pide cuidar: si el turno NO produce ningun mensaje, el aviso tiene que
// salir igual — el paciente se quedaria sin saber que el bot volvio.
func TestNoShow_NoticeStillSentWhenTurnHasNoMessages(t *testing.T) {
	pool, sm, sender, sess := escalatedPool(t)
	sess.Context["pre_escalation_state"] = "ASK_DOCUMENT"
	sm.resumeNoShowFn = func(_ context.Context, s *session.Session, target string) error {
		s.Status = session.StatusActive
		s.CurrentState = target
		return nil
	}
	pool.SetDependencies(sm, sender, &mockMessageProcessor{
		processFn: func(_ context.Context, _ *session.Session, _ bird.InboundMessage) (*statemachine.StateResult, error) {
			return statemachine.NewResult(statemachine.StateAskDocument), nil // sin mensajes
		},
	})

	pool.processAgentCommand(context.Background(), AgentCommand{Action: "no_show", Phone: "+573001234567"})

	sender.mu.Lock()
	defer sender.mu.Unlock()
	if len(sender.sent) != 1 {
		t.Fatalf("esperaba exactamente el aviso, hubo %d envios: %+v", len(sender.sent), sender.sent)
	}
	if sender.sent[0].text == "" {
		t.Error("el aviso de no-show no puede quedar vacio")
	}
}
