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

// Con la cola del próximo día aplicando, el aviso del caller («te voy a conectar… un momento»)
// contradice al del próximo día que manda el handler: el gancho lo descarta SIN enviarlo y
// devuelve true (devolver false lo dejaría para después del escalate, donde muere en 404).
func TestPreEscalationFlush_NectarQueuedDropsCallerNotice(t *testing.T) {
	pool, sender, sess := preEscalationPool("nectar:42")
	pool.SetEscalationQueuedNextDay(func(*session.Session) bool { return true })
	msgs := []statemachine.OutboundMessage{&statemachine.TextMessage{Text: statemachine.EscalationNoticeText}}

	if !pool.PreEscalationFlush(context.Background(), sess, msgs) {
		t.Fatal("con la cola el gancho devuelve true: los mensajes no deben quedar para después del escalate")
	}
	if len(sender.sent) != 0 {
		t.Fatalf("con la cola el aviso del caller no sale: %d envíos", len(sender.sent))
	}
}

// Sin la cola el predicado no cambia nada: el aviso del caller sale como siempre.
func TestPreEscalationFlush_NectarNotQueuedSends(t *testing.T) {
	pool, sender, sess := preEscalationPool("nectar:42")
	pool.SetEscalationQueuedNextDay(func(*session.Session) bool { return false })
	msgs := []statemachine.OutboundMessage{&statemachine.TextMessage{Text: statemachine.EscalationNoticeText}}

	if !pool.PreEscalationFlush(context.Background(), sess, msgs) || len(sender.sent) != 1 {
		t.Fatalf("sin cola el aviso sale: %d envíos", len(sender.sent))
	}
}

// Bird no consulta el predicado: declina como siempre.
func TestPreEscalationFlush_BirdIgnoresQueuePredicate(t *testing.T) {
	pool, sender, sess := preEscalationPool("+573001234567")
	asked := false
	pool.SetEscalationQueuedNextDay(func(*session.Session) bool { asked = true; return true })
	msgs := []statemachine.OutboundMessage{&statemachine.TextMessage{Text: "Te conecto con un asesor"}}

	if pool.PreEscalationFlush(context.Background(), sess, msgs) || len(sender.sent) != 0 || asked {
		t.Fatalf("Bird queda igual: sent=%d asked=%v", len(sender.sent), asked)
	}
}
