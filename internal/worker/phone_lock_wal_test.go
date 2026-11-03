package worker

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/bird"
)

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

// newLockContentionPool arma un pool con WAL falso y un candado de teléfono corto, y devuelve el
// teléfono ya bloqueado por "otro mensaje en vuelo".
func newLockContentionPool(t *testing.T) (*MessageWorkerPool, *mockSessionMgmt, *mockMessageSender, *fakeInboxMarker) {
	t.Helper()
	sm := newMockSessionMgmt()
	sender := &mockMessageSender{}
	inbox := &fakeInboxMarker{}
	pool := NewMessageWorkerPool(1, 10)
	pool.SetDependencies(sm, sender, &mockMessageProcessor{})
	pool.SetInboxRepo(inbox)
	pool.lockTimeout = 20 * time.Millisecond
	return pool, sm, sender, inbox
}

// B-01: un mensaje que no consiguió el candado del teléfono NO se procesó, así que su fila del WAL
// debe quedar 'pending' para que el replay de atascados lo recupere (antes se marcaba 'done' y la
// segunda foto de una orden se perdía).
func TestProcessMessage_PhoneLockTimeout_DoesNotMarkDone(t *testing.T) {
	pool, sm, sender, inbox := newLockContentionPool(t)
	const phone = "+573001234567"
	if err := sm.mutex.Lock(context.Background(), phone); err != nil {
		t.Fatal(err)
	}

	pool.processMessage(context.Background(), bird.InboundMessage{ID: "photo-2", Phone: phone, MessageType: "image"})

	if n := inbox.doneCount(); n != 0 {
		t.Fatalf("el mensaje que no tomó el candado no debe cerrarse en el WAL, got %d MarkDone", n)
	}
	if len(sender.sent) != 1 {
		t.Fatalf("el aviso al paciente se mantiene, got %d envíos", len(sender.sent))
	}

	// El replay lo trae de vuelta con el candado ya libre: ahora sí se procesa y se cierra.
	sm.mutex.Unlock(phone)
	pool.processMessage(context.Background(), bird.InboundMessage{ID: "photo-2", Phone: phone, MessageType: "image"})
	if n := inbox.doneCount(); n != 1 {
		t.Fatalf("procesado en el replay debe cerrarse una vez, got %d MarkDone", n)
	}
}

// Tope: un teléfono que sigue bloqueado en cada replay no se reintenta sin fin; al agotar los
// vencimientos la fila se cierra (como antes del arreglo) y no vuelve a avisar al paciente.
func TestProcessMessage_PhoneLockTimeout_GivesUpAfterMaxMisses(t *testing.T) {
	pool, sm, sender, inbox := newLockContentionPool(t)
	const phone = "+573001234567"
	if err := sm.mutex.Lock(context.Background(), phone); err != nil {
		t.Fatal(err)
	}
	msg := bird.InboundMessage{ID: "stuck-1", Phone: phone, MessageType: "text", Text: "hola"}

	for i := 1; i < maxPhoneLockMisses; i++ {
		pool.processMessage(context.Background(), msg)
		if n := inbox.doneCount(); n != 0 {
			t.Fatalf("vencimiento %d de %d: la fila debe seguir pending, got %d MarkDone", i, maxPhoneLockMisses, n)
		}
	}
	pool.processMessage(context.Background(), msg)
	if n := inbox.doneCount(); n != 1 {
		t.Fatalf("al agotar %d vencimientos la fila debe cerrarse, got %d MarkDone", maxPhoneLockMisses, n)
	}
	if _, ok := pool.lockMisses.Load(msg.ID); ok {
		t.Error("el contador de vencimientos debe liberarse al cerrar la fila")
	}
	if len(sender.sent) != maxPhoneLockMisses {
		t.Errorf("un aviso por vencimiento, got %d", len(sender.sent))
	}
}
