package worker

import (
	"context"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/session"
	"github.com/neuro-bot/neuro-bot/internal/statemachine"
)

// sendAndSave es el chokepoint de envío: debe coalescer los mensajes acumulados del turno
// (texto de transición + prompt siguiente) en un solo envío cobrado.
func TestSendAndSave_CoalescesTurnMessages(t *testing.T) {
	sender := &mockMessageSender{}
	pool := NewMessageWorkerPool(1, 10)
	pool.SetDependencies(&mockSessionMgmt{}, sender, &mockMessageProcessor{})
	sess := &session.Session{ID: "s-coal", PhoneNumber: "+573001234567"}

	pool.sendAndSave(context.Background(), sess, sess.PhoneNumber, &statemachine.StateResult{
		NextState: "ASK_ENTITY_NUMBER",
		Messages: []statemachine.OutboundMessage{
			&statemachine.TextMessage{Text: "✅ ¡Registro exitoso!"},
			&statemachine.ListMessage{
				Body:     "Selecciona tu entidad",
				Title:    "Entidades",
				Sections: []statemachine.ListSection{{Title: "S", Rows: []statemachine.ListRow{{ID: "1", Title: "EPS X"}}}},
			},
		},
	})

	total := countSent(sender, "text") + countSent(sender, "list")
	if total != 1 {
		t.Fatalf("el turno debe salir en 1 solo envío fusionado, hubo %d", total)
	}
	if countSent(sender, "list") != 1 {
		t.Errorf("el mensaje fusionado debe ser la lista (con el texto en el body)")
	}
}
