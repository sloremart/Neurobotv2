package notifications

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// hungPersister cuelga FindExpired hasta que se cancele su ctx (MySQL que no responde).
type hungPersister struct {
	mockPersister
	calls atomic.Int32
}

func (h *hungPersister) FindExpired(ctx context.Context) ([]PendingRow, error) {
	h.calls.Add(1)
	<-ctx.Done()
	return nil, ctx.Err()
}

// D-07: la pasada del checker de expiraciones termina al vencer su plazo y la siguiente arranca.
func TestRunExpirationPass_HungQueryEndsAtDeadline(t *testing.T) {
	p := &hungPersister{}
	m := &NotificationManager{persister: p, passTimeout: 20 * time.Millisecond}

	for pass := 1; pass <= 2; pass++ {
		done := make(chan struct{})
		go func() {
			m.runExpirationPass(context.Background())
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatalf("la pasada %d no terminó al vencer su plazo", pass)
		}
	}
	if got := p.calls.Load(); got != 2 {
		t.Errorf("FindExpired: esperaba 2 pasadas, got %d", got)
	}
}
