package monitor

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// urgentRig arma un UrgentAlerter con reloj manual y un envío que guarda los textos.
type urgentRig struct {
	mu    sync.Mutex
	sent  []string
	clock time.Time
	a     *UrgentAlerter
}

func newUrgentRig(cooldown time.Duration) *urgentRig {
	r := &urgentRig{clock: time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)}
	r.a = NewUrgentAlerter(func(_ context.Context, text string) error {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.sent = append(r.sent, text)
		return nil
	}, "docs/GUIA-VOLVER-A-BIRD.md", cooldown, func() time.Time {
		r.mu.Lock()
		defer r.mu.Unlock()
		return r.clock
	})
	return r
}

func (r *urgentRig) advance(d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clock = r.clock.Add(d)
}

func (r *urgentRig) sentCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.sent)
}

func TestUrgentFire_TextHasUrgentTriggerDetailAndGuide(t *testing.T) {
	r := newUrgentRig(10 * time.Minute)
	r.a.Fire(context.Background(), TriggerNectarDown, "5 fallos seguidos")
	if r.sentCount() != 1 {
		t.Fatalf("un envío, hubo %d", r.sentCount())
	}
	text := r.sent[0]
	for _, want := range []string{"URGENTE", "volver a Bird", string(TriggerNectarDown), "5 fallos seguidos", "docs/GUIA-VOLVER-A-BIRD.md"} {
		if !strings.Contains(text, want) {
			t.Errorf("el aviso no contiene %q: %s", want, text)
		}
	}
}

func TestUrgentFire_CooldownPerTrigger(t *testing.T) {
	r := newUrgentRig(10 * time.Minute)
	ctx := context.Background()
	r.a.Fire(ctx, TriggerProactiveStopped, "a")
	r.advance(5 * time.Minute)
	r.a.Fire(ctx, TriggerProactiveStopped, "b")
	if r.sentCount() != 1 {
		t.Fatalf("dentro del enfriamiento: 1 envío, hubo %d", r.sentCount())
	}
	// Otro disparador no queda bloqueado por el primero.
	r.a.Fire(ctx, TriggerEscalationFailing, "c")
	if r.sentCount() != 2 {
		t.Fatalf("disparadores distintos no se bloquean: 2 envíos, hubo %d", r.sentCount())
	}
	r.advance(5 * time.Minute)
	r.a.Fire(ctx, TriggerProactiveStopped, "d")
	if r.sentCount() != 3 {
		t.Fatalf("tras el enfriamiento repite: 3 envíos, hubo %d", r.sentCount())
	}
}

func TestUrgentCount_ThresholdInWindow(t *testing.T) {
	r := newUrgentRig(10 * time.Minute)
	ctx := context.Background()
	r.a.Count(ctx, TriggerNectarDown, 3, 5*time.Minute, "x")
	r.advance(time.Minute)
	r.a.Count(ctx, TriggerNectarDown, 3, 5*time.Minute, "x")
	if r.sentCount() != 0 {
		t.Fatalf("2 de 3: sin envío, hubo %d", r.sentCount())
	}
	r.advance(time.Minute)
	r.a.Count(ctx, TriggerNectarDown, 3, 5*time.Minute, "último: status 502")
	if r.sentCount() != 1 {
		t.Fatalf("3 de 3: un envío, hubo %d", r.sentCount())
	}
	if !strings.Contains(r.sent[0], "último: status 502") || !strings.Contains(r.sent[0], "3 en los últimos 5m0s") {
		t.Errorf("el aviso dice cuántos, en qué ventana y el detalle: %s", r.sent[0])
	}
}

func TestUrgentCount_OldHitsOutsideWindowDoNotCount(t *testing.T) {
	r := newUrgentRig(time.Minute)
	ctx := context.Background()
	r.a.Count(ctx, TriggerEscalationFailing, 3, 5*time.Minute, "x")
	r.a.Count(ctx, TriggerEscalationFailing, 3, 5*time.Minute, "x")
	r.advance(6 * time.Minute)
	r.a.Count(ctx, TriggerEscalationFailing, 3, 5*time.Minute, "x")
	if r.sentCount() != 0 {
		t.Fatalf("las ocurrencias viejas no cuentan: sin envío, hubo %d", r.sentCount())
	}
	r.a.Count(ctx, TriggerEscalationFailing, 3, 5*time.Minute, "x")
	r.a.Count(ctx, TriggerEscalationFailing, 3, 5*time.Minute, "x")
	if r.sentCount() != 1 {
		t.Fatalf("3 dentro de la ventana: un envío, hubo %d", r.sentCount())
	}
}

func TestUrgentAlerter_NilReceiverIsNoop(_ *testing.T) {
	var a *UrgentAlerter
	a.Fire(context.Background(), TriggerNectarDown, "x")
	a.Count(context.Background(), TriggerNectarDown, 1, time.Minute, "x")
}

// El aviso sale aunque el contexto de quien lo dispara ya esté cancelado (una petición a Néctar que
// venció por tiempo es justo la señal que se avisa).
func TestUrgentFire_SurvivesCanceledContext(t *testing.T) {
	var gotErr error
	a := NewUrgentAlerter(func(ctx context.Context, _ string) error {
		gotErr = ctx.Err()
		return nil
	}, "docs/GUIA-VOLVER-A-BIRD.md", time.Minute, time.Now)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a.Fire(ctx, TriggerNectarDown, "x")
	if gotErr != nil {
		t.Fatalf("el envío no hereda la cancelación: %v", gotErr)
	}
}

// Concurrencia: Count y Fire desde varias goroutines (go test -race).
func TestUrgentAlerter_Concurrent(t *testing.T) {
	r := newUrgentRig(time.Hour)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.a.Count(context.Background(), TriggerNectarDown, 5, time.Minute, "x")
			r.a.Fire(context.Background(), TriggerRemindersUnsent, "y")
		}()
	}
	wg.Wait()
	if r.sentCount() != 2 {
		t.Fatalf("un aviso por disparador dentro del enfriamiento: %d", r.sentCount())
	}
}
