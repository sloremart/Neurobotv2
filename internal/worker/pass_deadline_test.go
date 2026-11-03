package worker

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/bird"
)

// hungStaleSource cuelga la consulta de atascados hasta que se cancele su ctx.
type hungStaleSource struct{ calls atomic.Int32 }

func (h *hungStaleSource) PendingOlderThan(ctx context.Context, _ int) ([]bird.InboundMessage, error) {
	h.calls.Add(1)
	<-ctx.Done()
	return nil, ctx.Err()
}

// D-07: la pasada del replay de atascados termina al vencer su plazo y la siguiente arranca.
func TestRunStaleReplayPass_HungQueryEndsAtDeadline(t *testing.T) {
	pool := NewMessageWorkerPool(1, 10)
	pool.passTimeout = 20 * time.Millisecond
	src := &hungStaleSource{}

	for pass := 1; pass <= 2; pass++ {
		done := make(chan struct{})
		go func() {
			pool.runStaleReplayPass(context.Background(), src)
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatalf("la pasada %d no terminó al vencer su plazo", pass)
		}
	}
	if got := src.calls.Load(); got != 2 {
		t.Errorf("PendingOlderThan: esperaba 2 pasadas, got %d", got)
	}
}
