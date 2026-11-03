package worker

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/session"
	"github.com/neuro-bot/neuro-bot/internal/statemachine"
)

const lockBusyNotice = "Tu mensaje anterior está siendo procesado. Por favor espera un momento antes de enviar otro mensaje."

// fakeInboxMarker registra qué filas del WAL se cerraron.
type fakeInboxMarker struct {
	mu   sync.Mutex
	done []string
}

func (f *fakeInboxMarker) MarkDone(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.done = append(f.done, id)
	return nil
}

func (f *fakeInboxMarker) doneCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.done)
}

func (m *mockMessageSender) countText(text string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, s := range m.sent {
		if s.text == text {
			n++
		}
	}
	return n
}

type lockContention struct {
	pool      *MessageWorkerPool
	sm        *mockSessionMgmt
	sender    *mockMessageSender
	inbox     *fakeInboxMarker
	processed atomic.Int32
}

// newLockContention arma un pool con WAL falso y candado de teléfono corto, con el teléfono ya
// bloqueado por "otro mensaje en vuelo".
func newLockContention(t *testing.T, queueSize int, lockTimeout time.Duration) *lockContention {
	t.Helper()
	lc := &lockContention{sm: newMockSessionMgmt(), sender: &mockMessageSender{}, inbox: &fakeInboxMarker{}}
	processor := &mockMessageProcessor{
		processFn: func(context.Context, *session.Session, bird.InboundMessage) (*statemachine.StateResult, error) {
			lc.processed.Add(1)
			return &statemachine.StateResult{NextState: "GREETING"}, nil
		},
	}
	lc.pool = NewMessageWorkerPool(1, queueSize)
	lc.pool.SetDependencies(lc.sm, lc.sender, processor)
	lc.pool.SetInboxRepo(lc.inbox)
	lc.pool.lockTimeout = lockTimeout
	if err := lc.sm.mutex.Lock(context.Background(), testPhone); err != nil {
		t.Fatal(err)
	}
	return lc
}

const testPhone = "+573001234567"

// runUntilDone arranca el pool, encola msg, libera el candado tras `release` y espera a que la
// fila del WAL se cierre (o vence el plazo).
func (lc *lockContention) runUntilDone(t *testing.T, msg bird.InboundMessage, release time.Duration) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lc.pool.Start(ctx)
	if !lc.pool.Enqueue(msg) {
		t.Fatal("el primer encolado debe pasar")
	}
	time.Sleep(release)
	lc.sm.mutex.Unlock(testPhone)

	deadline := time.Now().Add(5 * time.Second)
	for lc.inbox.doneCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond) // margen para ver un cierre o proceso de más
}

// B-01 (a): el mensaje que venció el candado se reencola y se procesa apenas se libera, sin
// esperar al replay de atascados; su fila se cierra una sola vez.
func TestProcessMessage_PhoneLockTimeout_RequeuesAndProcessesOnRelease(t *testing.T) {
	lc := newLockContention(t, 10, 100*time.Millisecond)
	lc.runUntilDone(t, bird.InboundMessage{ID: "photo-2", Phone: testPhone, MessageType: "image"}, 150*time.Millisecond)

	if n := lc.inbox.doneCount(); n != 1 {
		t.Fatalf("la fila debe cerrarse una vez al procesarse, got %d MarkDone", n)
	}
	if n := lc.processed.Load(); n != 1 {
		t.Fatalf("el mensaje debe procesarse una vez, got %d", n)
	}
	if _, ok := lc.pool.lockMisses.Load("photo-2"); ok {
		t.Error("el contador de vencimientos debe liberarse al tomar el candado")
	}
}

// B-01 (b): aunque el candado venza dos veces, el aviso al paciente sale una sola vez.
func TestProcessMessage_PhoneLockTimeout_NoticeOncePerMessage(t *testing.T) {
	lc := newLockContention(t, 10, 100*time.Millisecond)
	lc.runUntilDone(t, bird.InboundMessage{ID: "photo-3", Phone: testPhone, MessageType: "image"}, 250*time.Millisecond)

	if n := lc.sender.countText(lockBusyNotice); n != 1 {
		t.Fatalf("el aviso debe salir una vez por mensaje, got %d", n)
	}
	if n := lc.processed.Load(); n != 1 {
		t.Fatalf("el mensaje debe procesarse una vez, got %d", n)
	}
	if n := lc.inbox.doneCount(); n != 1 {
		t.Fatalf("la fila debe cerrarse una vez, got %d MarkDone", n)
	}
}

// B-01 (c): con la cola llena no se bloquea ni se reencola; la fila queda 'pending' para el replay.
func TestProcessMessage_PhoneLockTimeout_QueueFullLeavesPending(t *testing.T) {
	lc := newLockContention(t, 1, 20*time.Millisecond)
	lc.pool.queue <- bird.InboundMessage{ID: "filler", Phone: "+573009999999"}

	finished := make(chan struct{})
	go func() {
		lc.pool.processMessage(context.Background(), bird.InboundMessage{ID: "photo-4", Phone: testPhone, MessageType: "image"})
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("con la cola llena processMessage no debe bloquearse")
	}
	if n := lc.inbox.doneCount(); n != 0 {
		t.Fatalf("la fila debe quedar pending para el replay, got %d MarkDone", n)
	}
	if got := (<-lc.pool.queue).ID; got != "filler" || len(lc.pool.queue) != 0 {
		t.Fatalf("no debe reencolarse con la cola llena (cabeza %q, quedan %d)", got, len(lc.pool.queue))
	}
}

// B-01 (apagado): con el ctx de la app cancelado no se reencola (Stop no espera por él) y la fila
// queda 'pending' para el replay de arranque.
func TestProcessMessage_PhoneLockTimeout_ShutdownLeavesPending(t *testing.T) {
	lc := newLockContention(t, 10, 20*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	lc.pool.processMessage(ctx, bird.InboundMessage{ID: "photo-5", Phone: testPhone, MessageType: "image"})

	if n := len(lc.pool.queue); n != 0 {
		t.Fatalf("apagando no debe reencolarse, got %d en cola", n)
	}
	if n := lc.inbox.doneCount(); n != 0 {
		t.Fatalf("la fila debe quedar pending para el replay de arranque, got %d MarkDone", n)
	}
}

// B-01 (d): al tercer vencimiento del mismo mensaje se rinde: cierra la fila, libera el contador y
// no avisa de nuevo al paciente.
func TestProcessMessage_PhoneLockTimeout_GivesUpAfterMaxMisses(t *testing.T) {
	lc := newLockContention(t, 10, 20*time.Millisecond)
	msg := bird.InboundMessage{ID: "stuck-1", Phone: testPhone, MessageType: "text", Text: "hola"}

	for i := 1; i < maxPhoneLockMisses; i++ {
		lc.pool.processMessage(context.Background(), msg)
		if n := lc.inbox.doneCount(); n != 0 {
			t.Fatalf("vencimiento %d de %d: la fila debe seguir pending, got %d MarkDone", i, maxPhoneLockMisses, n)
		}
		select {
		case requeued := <-lc.pool.queue:
			if requeued.ID != msg.ID {
				t.Fatalf("vencimiento %d: esperaba el mensaje reencolado, got %q", i, requeued.ID)
			}
		default:
			t.Fatalf("vencimiento %d: el mensaje debió reencolarse", i)
		}
	}
	lc.pool.processMessage(context.Background(), msg)
	if n := lc.inbox.doneCount(); n != 1 {
		t.Fatalf("al agotar %d vencimientos la fila debe cerrarse, got %d MarkDone", maxPhoneLockMisses, n)
	}
	if n := len(lc.pool.queue); n != 0 {
		t.Fatalf("al rendirse no debe reencolarse, got %d en cola", n)
	}
	if _, ok := lc.pool.lockMisses.Load(msg.ID); ok {
		t.Error("el contador de vencimientos debe liberarse al cerrar la fila")
	}
	if n := lc.sender.countText(lockBusyNotice); n != 1 {
		t.Errorf("el aviso debe salir solo en el primer vencimiento, got %d", n)
	}
}
