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

// slowResolvePersister devuelve filas viejas aún sin resolver y tarda en resolver la primera.
type slowResolvePersister struct {
	mockPersister
	delay    time.Duration
	resolved map[string]error // phone → ctx.Err() con que se resolvió
}

func (s *slowResolvePersister) FindExpired(context.Context) ([]PendingRow, error) {
	var out []PendingRow
	for _, phone := range []string{"+573000000001", "+573000000002"} {
		if _, done := s.resolved[phone]; !done {
			out = append(out, PendingRow{Phone: phone})
		}
	}
	return out, nil
}

func (s *slowResolvePersister) Resolve(ctx context.Context, phone, _ string) error {
	time.Sleep(s.delay)
	s.resolved[phone] = ctx.Err()
	return ctx.Err()
}

// D-07: la fila que cruza el plazo de la pasada se resuelve con un ctx vivo; la siguiente no
// arranca en esa pasada y sí en la siguiente.
func TestRunExpirationPass_RowOutlivesPassDeadline(t *testing.T) {
	p := &slowResolvePersister{delay: 60 * time.Millisecond, resolved: map[string]error{}}
	m := &NotificationManager{persister: p, passTimeout: 20 * time.Millisecond}

	m.runExpirationPass(context.Background())
	if err, ok := p.resolved["+573000000001"]; !ok || err != nil {
		t.Fatalf("pasada 1: la fila 1 debía resolverse con ctx vivo (ok=%v, err=%v)", ok, err)
	}
	if _, ok := p.resolved["+573000000002"]; ok {
		t.Fatal("pasada 1: la fila 2 no debía arrancar tras vencer la pasada")
	}
	p.delay = 0
	m.runExpirationPass(context.Background())
	if err, ok := p.resolved["+573000000002"]; !ok || err != nil {
		t.Fatalf("pasada 2: la fila 2 debía resolverse (ok=%v, err=%v)", ok, err)
	}
}
