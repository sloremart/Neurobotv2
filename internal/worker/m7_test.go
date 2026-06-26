package worker

import (
	"context"
	"errors"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/bird"
)

// --- Parte 1: el drop por backpressure libera el claim de dedup (M7) ---

func TestEnqueue_DropReleasesDedupClaim(t *testing.T) {
	pool := NewMessageWorkerPool(1, 1) // cola capacidad 1, sin workers arrancados → nada drena
	msg1 := bird.InboundMessage{ID: "m1", Phone: "+573001234567"}
	msg2 := bird.InboundMessage{ID: "m2", Phone: "+573001234567"}

	if !pool.Enqueue(msg1) {
		t.Fatal("msg1 debería encolar (llena la cola)")
	}
	// Forzar el camino de drop: cola llena + overflow al tope.
	pool.activeOverflow.Store(int32(maxOverflowGoroutines))

	if pool.Enqueue(msg2) {
		t.Fatal("msg2 debería descartarse (cola llena + overflow al tope)")
	}
	// M7: el claim de dedup de msg2 debe haberse liberado.
	if _, exists := pool.recentMessages.Load("m2"); exists {
		t.Error("el mensaje descartado NO debe quedar en el mapa de dedup")
	}

	// Re-encolar msg2 debe funcionar ahora (drenamos la cola y bajamos el overflow).
	<-pool.queue
	pool.activeOverflow.Store(0)
	if !pool.Enqueue(msg2) {
		t.Error("msg2 debería re-encolar tras liberar el claim")
	}
}

func TestEnqueue_DuplicateStillRejected(t *testing.T) {
	pool := NewMessageWorkerPool(1, 10)
	msg := bird.InboundMessage{ID: "dup-1", Phone: "+573001234567"}
	if !pool.Enqueue(msg) {
		t.Fatal("el primer encolado debe pasar")
	}
	if pool.Enqueue(msg) {
		t.Error("el segundo con el mismo ID debe rechazarse como duplicado")
	}
}

// --- Parte 2: re-replay periódico de los 'pending' atascados ---

type fakeStaleSource struct {
	msgs  []bird.InboundMessage
	err   error
	calls int
}

func (f *fakeStaleSource) PendingOlderThan(_ context.Context, _ int) ([]bird.InboundMessage, error) {
	f.calls++
	return f.msgs, f.err
}

func TestReplayStale_ReenqueuesAndIsIdempotent(t *testing.T) {
	pool := NewMessageWorkerPool(1, 10) // sin workers → los mensajes quedan en la cola
	src := &fakeStaleSource{msgs: []bird.InboundMessage{{ID: "s1"}, {ID: "s2"}}}

	pool.replayStale(context.Background(), src)
	if size, _ := pool.QueueStats(); size != 2 {
		t.Fatalf("esperaba 2 re-encolados, got %d", size)
	}
	// Segunda corrida con los MISMOS IDs: el dedup evita el doble encolado.
	pool.replayStale(context.Background(), src)
	if size, _ := pool.QueueStats(); size != 2 {
		t.Errorf("idempotencia: esperaba seguir en 2 (dedup), got %d", size)
	}
}

func TestReplayStale_EmptyAndError(t *testing.T) {
	pool := NewMessageWorkerPool(1, 10)

	pool.replayStale(context.Background(), &fakeStaleSource{}) // sin mensajes
	if size, _ := pool.QueueStats(); size != 0 {
		t.Errorf("vacío: esperaba 0, got %d", size)
	}

	pool.replayStale(context.Background(), &fakeStaleSource{err: errors.New("db down")}) // error de query
	if size, _ := pool.QueueStats(); size != 0 {
		t.Errorf("error: esperaba 0 (no encola), got %d", size)
	}
}
