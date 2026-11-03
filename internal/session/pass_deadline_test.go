package session

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// D-07: con MySQL colgado (la consulta no vuelve hasta que se cancele su ctx), la pasada del
// checker termina al vencer su plazo y la siguiente arranca: el atomic.Bool no queda tomado.
func TestRunInactivityPass_HungQueryEndsAtDeadline(t *testing.T) {
	var inactiveCalls, escalatedCalls atomic.Int32
	repo := &mockRepo{
		findInactiveFn: func(ctx context.Context, _ int) ([]InactiveSession, error) {
			inactiveCalls.Add(1)
			<-ctx.Done()
			return nil, ctx.Err()
		},
		findEscalatedFn: func(ctx context.Context) ([]EscalatedSession, error) {
			escalatedCalls.Add(1)
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	mgr := NewSessionManager(repo, 120)
	mgr.passTimeout = 20 * time.Millisecond
	deps := InactivityDeps{ReminderMin: 5, CloseMin: 15}

	for pass := 1; pass <= 2; pass++ {
		done := make(chan struct{})
		go func() {
			mgr.runInactivityPass(context.Background(), deps)
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatalf("la pasada %d no terminó al vencer su plazo", pass)
		}
	}
	if got := inactiveCalls.Load(); got != 2 {
		t.Errorf("FindInactiveSessions: esperaba 2 pasadas, got %d", got)
	}
	// Cada consulta tiene su propio plazo: la de activas colgada no deja sin pasada a las escaladas.
	if got := escalatedCalls.Load(); got != 2 {
		t.Errorf("FindEscalatedSessions: esperaba 2 pasadas, got %d", got)
	}
}
