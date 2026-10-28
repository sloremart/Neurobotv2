package worker

import (
	"context"
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/session"
	"github.com/neuro-bot/neuro-bot/internal/statemachine"
)

// sendAndSave aplica el vencimiento extendido que pidió el paso (la escalación encolada para el
// próximo día hábil), DESPUÉS de guardar el estado, y no lo aplica si no se pidió.
func TestSendAndSave_AppliesExtendExpiryUntil(t *testing.T) {
	mgmt := newMockSessionMgmt()
	var got []time.Time
	mgmt.extendExpiryFn = func(_ context.Context, _ *session.Session, until time.Time) error {
		got = append(got, until)
		return nil
	}
	pool := NewMessageWorkerPool(1, 10)
	pool.SetDependencies(mgmt, &mockMessageSender{}, &mockMessageProcessor{
		processFn: func(context.Context, *session.Session, bird.InboundMessage) (*statemachine.StateResult, error) {
			return statemachine.NewResult(statemachine.StateEscalated), nil
		},
	})

	sess := &session.Session{ID: "sess-exp", PhoneNumber: "nectar:12", CurrentState: statemachine.StateEscalateToAgent}
	until := time.Date(2026, 10, 24, 15, 0, 0, 0, time.UTC)
	pool.sendAndSave(context.Background(), sess, sess.PhoneNumber,
		statemachine.NewResult(statemachine.StateEscalated).WithExpiryUntil(until))
	if len(got) != 1 || !got[0].Equal(until) {
		t.Fatalf("ExtendExpiry debía llamarse con %s: %v", until, got)
	}

	pool.sendAndSave(context.Background(), sess, sess.PhoneNumber, statemachine.NewResult(statemachine.StateEscalated))
	if len(got) != 1 {
		t.Fatalf("sin plazo pedido no se estira nada: %v", got)
	}
}
