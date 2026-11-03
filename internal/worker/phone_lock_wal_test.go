package worker

import (
	"context"
	"fmt"
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

// assertLockEntry: el mensaje se fue al replay y conserva su plazo y el aviso ya dado (R2-2).
func assertLockEntry(t *testing.T, pool *MessageWorkerPool, id string) {
	t.Helper()
	if _, ok := pool.lockMisses.Load(id); !ok {
		t.Errorf("la entrada de %q en lockMisses debe conservarse para el replay", id)
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
	assertLockEntry(t, lc.pool, "photo-4")
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
	assertLockEntry(t, lc.pool, "photo-5")
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
	assertLockEntry(t, lc.pool, "photo-6")
}

// B-01 (d): pasado maxPhoneLockWait desde el primer vencimiento se rinde: cierra la fila, no
// reencola, libera la entrada y no avisa de nuevo al paciente.
func TestProcessMessage_PhoneLockTimeout_GivesUpAfterMaxWait(t *testing.T) {
	lc := newLockContention(t, 10, 20*time.Millisecond)
	msg := bird.InboundMessage{ID: "stuck-1", Phone: testPhone, MessageType: "text", Text: "hola"}
	// primer intento fallido hace maxPhoneLockWait, con el aviso ya dado
	lc.pool.lockMisses.Store(msg.ID, lockMiss{first: time.Now().Add(-maxPhoneLockWait), notified: true})

	lc.pool.processMessage(context.Background(), msg)

	if n := lc.inbox.doneCount(); n != 1 {
		t.Fatalf("agotada la espera la fila debe cerrarse, got %d MarkDone", n)
	}
	assertNotRequeued(t, lc.pool)
	assertNoLockEntry(t, lc.pool, msg.ID)
	if n := lc.sender.countText(lockBusyNotice); n != 0 {
		t.Errorf("el aviso ya había salido, got %d", n)
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

// burstPool arma un pool de `workers` workers con el teléfono A ya bloqueado. Los mensajes de otros
// teléfonos se quedan dentro del procesador hasta que se cierre `hold`, para medir cuántos workers
// quedan libres a la vez.
type burstPool struct {
	*lockContention
	otherInside atomic.Int32
	hold        chan struct{}
}

func newBurstPool(t *testing.T, workers int, lockTimeout, retryDelay time.Duration) *burstPool {
	t.Helper()
	bp := &burstPool{lockContention: newLockContention(t, 100, lockTimeout), hold: make(chan struct{})}
	processor := &mockMessageProcessor{
		processFn: func(_ context.Context, _ *session.Session, msg bird.InboundMessage) (*statemachine.StateResult, error) {
			if msg.Phone != testPhone {
				bp.otherInside.Add(1)
				<-bp.hold
			}
			bp.processed.Add(1)
			return &statemachine.StateResult{NextState: "GREETING"}, nil
		},
	}
	bp.pool = NewMessageWorkerPool(workers, 100)
	bp.pool.SetDependencies(bp.sm, bp.sender, processor)
	bp.pool.SetInboxRepo(bp.inbox)
	bp.pool.lockTimeout = lockTimeout
	bp.pool.lockRetryDelay = retryDelay
	return bp
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("plazo vencido esperando: %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// B-01 (R2-1): una ráfaga de 5 mensajes de un teléfono con el candado tomado ocupa a lo sumo un
// worker en espera; los otros 4 atienden a la vez a otros teléfonos. Al liberar el candado se
// procesan todos los de la ráfaga y cada fila se cierra una sola vez.
func TestProcessMessage_PhoneLockBurst_OneWaiterAndAllProcessed(t *testing.T) {
	bp := newBurstPool(t, 5, 5*time.Second, 50*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bp.pool.Start(ctx)

	for i := 0; i < 5; i++ {
		bp.pool.Enqueue(bird.InboundMessage{ID: fmt.Sprintf("burst-%d", i), Phone: testPhone, MessageType: "image"})
	}
	time.Sleep(200 * time.Millisecond) // la ráfaga ya pasó por los workers y se difirió
	for i := 0; i < 4; i++ {
		bp.pool.Enqueue(bird.InboundMessage{ID: fmt.Sprintf("other-%d", i), Phone: fmt.Sprintf("+57300000000%d", i), Text: "hola"})
	}
	// Los 4 de otros teléfonos dentro del procesador A LA VEZ = 4 workers libres de 5.
	waitFor(t, "4 workers libres atendiendo otros teléfonos", func() bool { return bp.otherInside.Load() == 4 })
	close(bp.hold)

	bp.sm.mutex.Unlock(testPhone)
	waitFor(t, "las 9 filas cerradas", func() bool { return bp.inbox.doneCount() == 9 })
	time.Sleep(200 * time.Millisecond) // margen para ver un cierre de más
	// R3-1: toda la ráfaga se procesó antes de lockTimeout, así que nadie recibe el aviso.
	if n := bp.sender.countText(lockBusyNotice); n != 0 {
		t.Errorf("procesada antes de lockTimeout, la ráfaga no debe recibir avisos, got %d", n)
	}
	bp.inbox.mu.Lock()
	seen := map[string]int{}
	for _, id := range bp.inbox.done {
		seen[id]++
	}
	bp.inbox.mu.Unlock()
	for id, n := range seen {
		if n != 1 {
			t.Errorf("la fila %q se cerró %d veces", id, n)
		}
	}
	if len(seen) != 9 || bp.processed.Load() != 9 {
		t.Errorf("esperaba 9 mensajes procesados y cerrados, got %d filas y %d procesados", len(seen), bp.processed.Load())
	}
}

// R3-1: textos seguidos («hola», «buenas», «quiero cita») mientras otro mensaje tiene el candado
// menos de lockTimeout: se difieren o esperan, se procesan todos y ningún paciente recibe el aviso.
func TestProcessMessage_PhoneLockQuickTexts_NoNotice(t *testing.T) {
	bp := newBurstPool(t, 3, 2*time.Second, 20*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bp.pool.Start(ctx)

	for i, text := range []string{"hola", "buenas", "quiero cita"} {
		bp.pool.Enqueue(bird.InboundMessage{ID: fmt.Sprintf("quick-%d", i), Phone: testPhone, MessageType: "text", Text: text})
	}
	time.Sleep(300 * time.Millisecond) // el mensaje en vuelo aún tiene el candado
	bp.sm.mutex.Unlock(testPhone)
	waitFor(t, "las 3 filas cerradas", func() bool { return bp.inbox.doneCount() == 3 })
	time.Sleep(100 * time.Millisecond) // margen para ver un aviso de más

	if n := bp.processed.Load(); n != 3 {
		t.Errorf("los 3 textos deben procesarse, got %d", n)
	}
	if n := bp.sender.countText(lockBusyNotice); n != 0 {
		t.Errorf("procesados antes de lockTimeout no se avisa, got %d", n)
	}
}

// requeuedWithin espera a que el timer de un diferido reencole el mensaje (sin workers arrancados).
func requeuedWithin(t *testing.T, pool *MessageWorkerPool, id string) {
	t.Helper()
	select {
	case got := <-pool.queue:
		if got.ID != id {
			t.Fatalf("esperaba %q reencolado, got %q", id, got.ID)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("%q debió reencolarse", id)
	}
}

// R3-1: un diferido no avisa al diferirse; avisa una sola vez cuando ya lleva lockTimeout sin
// procesarse, por más veces que se difiera después, y se procesa al liberarse el candado.
func TestProcessMessage_PhoneLockDeferred_NoticeOnceAfterLockTimeout(t *testing.T) {
	lc := newLockContention(t, 10, 50*time.Millisecond)
	lc.pool.lockRetryDelay = time.Millisecond
	lc.pool.lockWaiters.Store(testPhone, struct{}{}) // otro worker ya espera el candado
	msg := bird.InboundMessage{ID: "deferred-1", Phone: testPhone, MessageType: "text", Text: "hola"}

	lc.pool.processMessage(context.Background(), msg)
	requeuedWithin(t, lc.pool, msg.ID)
	if n := lc.sender.countText(lockBusyNotice); n != 0 {
		t.Fatalf("diferido sin haber esperado lockTimeout no se avisa, got %d", n)
	}

	time.Sleep(60 * time.Millisecond) // ya lleva más de lockTimeout sin procesarse
	for i := 0; i < 2; i++ {
		lc.pool.processMessage(context.Background(), msg)
		requeuedWithin(t, lc.pool, msg.ID)
	}
	if n := lc.sender.countText(lockBusyNotice); n != 1 {
		t.Fatalf("pasado lockTimeout el diferido recibe exactamente un aviso, got %d", n)
	}

	lc.pool.lockWaiters.Delete(testPhone)
	lc.sm.mutex.Unlock(testPhone)
	lc.pool.processMessage(context.Background(), msg)
	if n := lc.processed.Load(); n != 1 {
		t.Fatalf("el mensaje debe procesarse una vez, got %d", n)
	}
	if n := lc.inbox.doneCount(); n != 1 {
		t.Fatalf("la fila debe cerrarse una vez, got %d MarkDone", n)
	}
	if n := lc.sender.countText(lockBusyNotice); n != 1 {
		t.Errorf("procesarlo no repite el aviso, got %d", n)
	}
	assertNoLockEntry(t, lc.pool, msg.ID)
}

// B-01 (R2-1): Stop con reencolados diferidos pendientes no se cuelga, y los timers que disparan
// después no meten nada en la cola: las filas quedan 'pending' para el replay de arranque.
func TestProcessMessage_PhoneLockBurst_StopWithPendingTimers(t *testing.T) {
	bp := newBurstPool(t, 3, 300*time.Millisecond, 200*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	bp.pool.Start(ctx)
	for i := 0; i < 3; i++ {
		bp.pool.Enqueue(bird.InboundMessage{ID: fmt.Sprintf("stop-%d", i), Phone: testPhone, MessageType: "image"})
	}
	time.Sleep(50 * time.Millisecond) // dos diferidos con su timer en marcha, uno esperando

	cancel()
	stopped := make(chan struct{})
	go func() {
		bp.pool.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop no debe quedarse esperando por los timers")
	}
	time.Sleep(400 * time.Millisecond) // los timers ya dispararon
	if n := len(bp.pool.queue); n != 0 {
		t.Fatalf("tras Stop un timer no debe encolar, got %d en cola", n)
	}
	if n := bp.inbox.doneCount(); n != 0 {
		t.Fatalf("las filas deben quedar pending para el replay de arranque, got %d MarkDone", n)
	}
}

// R2-2: la limpieza periódica purga la entrada de un mensaje que se fue al replay y no volvió, y no
// toca la de uno todavía dentro del plazo.
func TestPurgeStaleLockMisses(t *testing.T) {
	pool := NewMessageWorkerPool(1, 1)
	now := time.Now()
	pool.lockMisses.Store("gone", lockMiss{first: now.Add(-2*maxPhoneLockWait - time.Minute)})
	pool.lockMisses.Store("live", lockMiss{first: now.Add(-maxPhoneLockWait)})

	pool.purgeStaleLockMisses(now)

	assertNoLockEntry(t, pool, "gone")
	assertLockEntry(t, pool, "live")
}
