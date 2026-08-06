package worker

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/session"
	"github.com/neuro-bot/neuro-bot/internal/statemachine"
)

func retryTestPool(sender *mockMessageSender, sm *mockSessionMgmt) *MessageWorkerPool {
	pool := NewMessageWorkerPool(1, 10)
	pool.SetDependencies(sm, sender, &mockMessageProcessor{})
	pool.retryDelay = 20 * time.Millisecond
	return pool
}

func textResult() *statemachine.StateResult {
	return &statemachine.StateResult{
		NextState: "MAIN_MENU",
		Messages:  []statemachine.OutboundMessage{&statemachine.TextMessage{Text: "hola"}},
	}
}

// Un 4xx permanente (p.ej. 422 InvalidPayload) NO debe reintentarse: Bird cobra cada intento
// y el reintento con el mismo payload/destinatario está condenado a fallar igual.
func TestSendAndSave_NoRetryOnPermanent422(t *testing.T) {
	sender := &mockMessageSender{sendErr: &bird.APIError{Status: 422, Body: "InvalidPayload"}}
	pool := retryTestPool(sender, &mockSessionMgmt{})
	sess := &session.Session{ID: "s-perm", PhoneNumber: "+573001234567"}

	pool.sendAndSave(context.Background(), sess, sess.PhoneNumber, textResult())

	time.Sleep(200 * time.Millisecond) // 10× retryDelay: si hubiera retry ya habría corrido
	if got := countSent(sender, "text"); got != 1 {
		t.Fatalf("un 422 permanente no debe reintentarse: %d envíos", got)
	}
}

// Un error transitorio (red caída) SÍ debe conservar el reintento actual.
func TestSendAndSave_RetriesTransientError(t *testing.T) {
	sender := &mockMessageSender{sendErr: errors.New("dial tcp: connection refused")}
	pool := retryTestPool(sender, &mockSessionMgmt{})
	sess := &session.Session{ID: "s-net", PhoneNumber: "+573001234567"}

	pool.sendAndSave(context.Background(), sess, sess.PhoneNumber, textResult())

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if countSent(sender, "text") >= 2 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("un error de red debe reintentarse: %d envíos", countSent(sender, "text"))
}

// Un identificador no contactable marca la sesión (contexto persistido) para que el dashboard y
// la auditoría lo vean, y tampoco reintenta.
func TestSendAndSave_NonContactable_MarksSessionOnce(t *testing.T) {
	sender := &mockMessageSender{sendErr: fmt.Errorf("send: %w", bird.ErrNonContactable)}
	var savedCtx map[string]string
	sm := &mockSessionMgmt{}
	sm.saveFn = func(_ context.Context, s *session.Session, state string, updateCtx map[string]string, _ []string) error {
		savedCtx = updateCtx
		s.CurrentState = state
		return nil
	}
	pool := retryTestPool(sender, sm)
	sess := &session.Session{ID: "s-nc", PhoneNumber: "felipe.rubio"}

	pool.sendAndSave(context.Background(), sess, sess.PhoneNumber, textResult())

	if savedCtx["non_contactable"] != "1" {
		t.Fatalf("la sesión debe quedar marcada non_contactable=1 en el contexto persistido, got %v", savedCtx)
	}
	time.Sleep(200 * time.Millisecond)
	if got := countSent(sender, "text"); got != 1 {
		t.Fatalf("no contactable no debe reintentarse: %d envíos", got)
	}
}
