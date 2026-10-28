package worker

import (
	"context"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/session"
	"github.com/neuro-bot/neuro-bot/internal/statemachine"
)

func preEscalationPool(phone string) (*MessageWorkerPool, *mockMessageSender, *session.Session) {
	sender := &mockMessageSender{}
	pool := NewMessageWorkerPool(1, 10)
	pool.SetDependencies(newMockSessionMgmt(), sender, &mockMessageProcessor{})
	sess := &session.Session{ID: "s1", PhoneNumber: phone, ConversationID: "conv1", Context: map[string]string{}}
	return pool, sender, sess
}

// Con teléfono E.164 (Bird) el gancho declina: no envía nada y los mensajes salen al final
// del turno como siempre.
func TestPreEscalationFlush_BirdUnchanged(t *testing.T) {
	pool, sender, sess := preEscalationPool("+573001234567")
	msgs := []statemachine.OutboundMessage{&statemachine.TextMessage{Text: "Te conecto con un asesor"}}

	if pool.PreEscalationFlush(context.Background(), sess, msgs) {
		t.Fatal("en Bird el gancho devuelve false")
	}
	if len(sender.sent) != 0 {
		t.Fatalf("en Bird el gancho no envía: %d", len(sender.sent))
	}
}

// Un id de conversación numérico pelado no es una sesión de Néctar.
func TestPreEscalationFlush_BareNumericIsNotNectar(t *testing.T) {
	pool, sender, sess := preEscalationPool("12345")
	msgs := []statemachine.OutboundMessage{&statemachine.TextMessage{Text: "hola"}}

	if pool.PreEscalationFlush(context.Background(), sess, msgs) {
		t.Fatal("un id numérico pelado no es Néctar")
	}
	if len(sender.sent) != 0 {
		t.Fatalf("no debe enviar: %d", len(sender.sent))
	}
}

// En Néctar el gancho manda los mensajes coalescidos y devuelve true.
func TestPreEscalationFlush_NectarSendsCoalesced(t *testing.T) {
	pool, sender, sess := preEscalationPool("nectar:42")
	msgs := []statemachine.OutboundMessage{
		&statemachine.TextMessage{Text: "uno"},
		&statemachine.TextMessage{Text: "dos"},
	}

	if !pool.PreEscalationFlush(context.Background(), sess, msgs) {
		t.Fatal("en Néctar el gancho devuelve true")
	}
	if len(sender.sent) != 1 {
		t.Fatalf("dos textos se coalescen en un envío: %d", len(sender.sent))
	}
	if sender.sent[0].phone != "nectar:42" {
		t.Fatalf("el envío va a la clave de sesión: %q", sender.sent[0].phone)
	}
}
