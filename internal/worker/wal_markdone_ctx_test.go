package worker

import (
	"context"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/session"
	"github.com/neuro-bot/neuro-bot/internal/statemachine"
)

// ctxInboxMarker es un WAL falso que, como el de verdad, no cierra la fila con un ctx agotado.
type ctxInboxMarker struct {
	mu   sync.Mutex
	done []string
}

func (f *ctxInboxMarker) MarkDone(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.done = append(f.done, id)
	return nil
}

// N-01: un turno que pasa de processMsgTimeout cierra igual su fila del WAL. Con el ctx del turno,
// ya vencido, MarkDone fallaba y el replay de atascados reprocesaba el mensaje minutos después.
func TestProcessMessage_SlowTurnStillClosesWALRow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		inbox := &ctxInboxMarker{}
		processor := &mockMessageProcessor{
			processFn: func(context.Context, *session.Session, bird.InboundMessage) (*statemachine.StateResult, error) {
				time.Sleep(processMsgTimeout + time.Second) // reloj falso de la burbuja
				return &statemachine.StateResult{NextState: "GREETING"}, nil
			},
		}
		pool := NewMessageWorkerPool(1, 10)
		pool.SetDependencies(newMockSessionMgmt(), &mockMessageSender{}, processor)
		pool.SetInboxRepo(inbox)

		pool.processMessage(context.Background(), bird.InboundMessage{ID: "slow-1", Phone: testPhone, MessageType: "text", Text: "hola"})
		synctest.Wait()

		inbox.mu.Lock()
		defer inbox.mu.Unlock()
		if len(inbox.done) != 1 || inbox.done[0] != "slow-1" {
			t.Fatalf("la fila del turno lento debe cerrarse una vez, got %v", inbox.done)
		}
	})
}
