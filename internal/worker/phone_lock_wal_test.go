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

	deadline := time.Now().Add(10 * time.Second)
	for lc.inbox.doneCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond) // margen para ver un cierre o proceso de más
}

// requeued saca de la cola (sin workers arrancados) el mensaje reencolado, o falla.
func requeued(t *testing.T, pool *MessageWorkerPool, id string) {
	t.Helper()
	select {
	case got := <-pool.queue:
		if got.ID != id {
			t.Fatalf("esperaba %q reencolado, got %q", id, got.ID)
		}
	default:
		t.Fatalf("%q debió reencolarse", id)
	}
}

func assertNotRequeued(t *testing.T, pool *MessageWorkerPool) {
	t.Helper()
	if n := len(pool.queue); n != 0 {
		t.Fatalf("no debe reencolarse, got %d en cola", n)
	}
}

func assertNoLockEntry(t *testing.T, pool *MessageWorkerPool, id string) {
	t.Helper()
	if _, ok := pool.lockMisses.Load(id); ok {
		t.Errorf("la entrada de %q en lockMisses debe borrarse al salir el mensaje", id)
	}
}

// B-01 (a): el mensaje que venció el candado se reencola y se procesa apenas se libera, sin
// esperar al replay de atascados; su fila se cierra una sola vez.
func TestProcessMessage_PhoneLockTimeout_RequeuesAndProcessesOnRelease(t *testing.T) {
	lc := newLockContention(t, 10, 200*time.Millisecond)
	lc.runUntilDone(t, bird.InboundMessage{ID: "photo-2", Phone: testPhone, MessageType: "image"}, 500*time.Millisecond)

	if n := lc.inbox.doneCount(); n != 1 {
		t.Fatalf("la fila debe cerrarse una vez al procesarse, got %d MarkDone", n)
	}
	if n := lc.processed.Load(); n != 1 {
		t.Fatalf("el mensaje debe procesarse una vez, got %d", n)
	}
	if n := lc.sender.countText(lockBusyNotice); n != 1 {
		t.Errorf("el aviso debe salir una vez por mensaje, got %d", n)
	}
	assertNoLockEntry(t, lc.pool, "photo-2")
}

// B-01 (b): aunque el candado venza dos veces, el aviso al paciente sale una sola vez, y el
// reintento que consigue el candado procesa y cierra la fila.
func TestProcessMessage_PhoneLockTimeout_NoticeOncePerMessage(t *testing.T) {
	lc := newLockContention(t, 10, 20*time.Millisecond)
	msg := bird.InboundMessage{ID: "photo-3", Phone: testPhone, MessageType: "image"}

	for i := 0; i < 2; i++ {
		lc.pool.processMessage(context.Background(), msg)
		requeued(t, lc.pool, msg.ID)
	}
	if n := lc.sender.countText(lockBusyNotice); n != 1 {
		t.Fatalf("el aviso debe salir una vez aunque venza 2 veces, got %d", n)
	}
	if n := lc.inbox.doneCount(); n != 0 {
		t.Fatalf("reencolado, la fila sigue pending, got %d MarkDone", n)
	}

	lc.sm.mutex.Unlock(testPhone)
	lc.pool.processMessage(context.Background(), msg)
	if n := lc.processed.Load(); n != 1 {
		t.Fatalf("el mensaje debe procesarse una vez, got %d", n)
	}
	if n := lc.inbox.doneCount(); n != 1 {
		t.Fatalf("la fila debe cerrarse una vez, got %d MarkDone", n)
	}
	assertNoLockEntry(t, lc.pool, msg.ID)
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
	case <-time.After(5 * time.Second):
		t.Fatal("con la cola llena processMessage no debe bloquearse")
	}
	if n := lc.inbox.doneCount(); n != 0 {
		t.Fatalf("la fila debe quedar pending para el replay, got %d MarkDone", n)
	}
	if got := (<-lc.pool.queue).ID; got != "filler" || len(lc.pool.queue) != 0 {
		t.Fatalf("no debe reencolarse con la cola llena (cabeza %q, quedan %d)", got, len(lc.pool.queue))
	}
	assertNoLockEntry(t, lc.pool, "photo-4")
}

// B-01 (apagado): con el ctx de la app cancelado no se reencola (Stop no espera por él) y la fila
// queda 'pending' para el replay de arranque.
func TestProcessMessage_PhoneLockTimeout_ShutdownLeavesPending(t *testing.T) {
	lc := newLockContention(t, 10, 20*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	lc.pool.processMessage(ctx, bird.InboundMessage{ID: "photo-5", Phone: testPhone, MessageType: "image"})

	assertNotRequeued(t, lc.pool)
	if n := lc.inbox.doneCount(); n != 0 {
		t.Fatalf("la fila debe quedar pending para el replay de arranque, got %d MarkDone", n)
	}
	assertNoLockEntry(t, lc.pool, "photo-5")
}

// B-01 (drain de Stop): con el cierre ya marcado (el ctx del drain no está cancelado) tampoco se
// reencola, así el drain no entra en bucle; la fila queda 'pending'.
func TestProcessMessage_PhoneLockTimeout_ClosingLeavesPending(t *testing.T) {
	lc := newLockContention(t, 10, 20*time.Millisecond)
	lc.pool.closeMu.Lock()
	lc.pool.closing = true
	lc.pool.closeMu.Unlock()

	lc.pool.processMessage(context.Background(), bird.InboundMessage{ID: "photo-6", Phone: testPhone, MessageType: "image"})

	assertNotRequeued(t, lc.pool)
	if n := lc.inbox.doneCount(); n != 0 {
		t.Fatalf("la fila debe quedar pending para el replay de arranque, got %d MarkDone", n)
	}
	assertNoLockEntry(t, lc.pool, "photo-6")
}

// B-01 (d): pasado maxPhoneLockWait desde el primer vencimiento se rinde: cierra la fila, no
// reencola, libera la entrada y no avisa de nuevo al paciente.
func TestProcessMessage_PhoneLockTimeout_GivesUpAfterMaxWait(t *testing.T) {
	lc := newLockContention(t, 10, 20*time.Millisecond)
	msg := bird.InboundMessage{ID: "stuck-1", Phone: testPhone, MessageType: "text", Text: "hola"}
	lc.pool.lockMisses.Store(msg.ID, time.Now().Add(-maxPhoneLockWait)) // primer vencimiento hace maxPhoneLockWait

	lc.pool.processMessage(context.Background(), msg)

	if n := lc.inbox.doneCount(); n != 1 {
		t.Fatalf("agotada la espera la fila debe cerrarse, got %d MarkDone", n)
	}
	assertNotRequeued(t, lc.pool)
	assertNoLockEntry(t, lc.pool, msg.ID)
	if n := lc.sender.countText(lockBusyNotice); n != 0 {
		t.Errorf("el aviso ya salió en el primer vencimiento, got %d", n)
	}
}

// B-01: lo que no rastrea el WAL (virtual-, sin id) conserva la conducta de antes: aviso en cada
// vencimiento, sin reencolar ni contar.
func TestProcessMessage_PhoneLockTimeout_UntrackedKeepsOldBehavior(t *testing.T) {
	lc := newLockContention(t, 10, 20*time.Millisecond)
	for _, id := range []string{"virtual-wl-1", "virtual-wl-1", ""} {
		lc.pool.processMessage(context.Background(), bird.InboundMessage{ID: id, Phone: testPhone, Text: "__waiting_list_trigger__"})
		assertNotRequeued(t, lc.pool)
		assertNoLockEntry(t, lc.pool, id)
	}
	if n := lc.sender.countText(lockBusyNotice); n != 3 {
		t.Errorf("aviso en cada vencimiento, como antes, got %d", n)
	}
	if n := lc.inbox.doneCount(); n != 0 {
		t.Errorf("sin fila en el WAL no hay MarkDone, got %d", n)
	}
}

// B-01: al reencolar se renueva el claim de dedup, así el replay de atascados no mete una segunda
// copia aunque el reencolado dure más que dedupTTL.
func TestProcessMessage_PhoneLockTimeout_RenewsDedupClaim(t *testing.T) {
	lc := newLockContention(t, 10, 20*time.Millisecond)
	msg := bird.InboundMessage{ID: "photo-7", Phone: testPhone, MessageType: "image"}
	lc.pool.recentMessages.Store(msg.ID, time.Now().Add(-dedupTTL-time.Minute)) // claim ya caducado
	lc.pool.dedupCount.Add(1)

	lc.pool.processMessage(context.Background(), msg)
	requeued(t, lc.pool, msg.ID)

	v, ok := lc.pool.recentMessages.Load(msg.ID)
	if !ok || time.Since(v.(time.Time)) > time.Minute {
		t.Fatalf("el claim de dedup debe renovarse al reencolar, got %v (presente=%v)", v, ok)
	}
	if lc.pool.Enqueue(msg) {
		t.Error("con el claim renovado, el replay de atascados no debe poder reencolarlo")
	}
	if n := lc.pool.dedupCount.Load(); n != 1 {
		t.Errorf("renovar un claim existente no cambia dedupCount, got %d", n)
	}
}
