package monitor

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// urgentRig arma un UrgentAlerter con reloj manual, su Start corriendo y un envío que guarda los textos.
type urgentRig struct {
	mu    sync.Mutex
	sent  []string
	clock time.Time
	a     *UrgentAlerter
}

func newUrgentRig(t *testing.T, cooldown time.Duration) *urgentRig {
	t.Helper()
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
	startAlerter(t, r.a)
	return r
}

// startAlerter corre Start hasta el fin de la prueba.
func startAlerter(t *testing.T, a *UrgentAlerter) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { a.Start(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
}

func (r *urgentRig) advance(d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clock = r.clock.Add(d)
}

// expectSent espera (el envío es asíncrono) a que haya exactamente n envíos y que no llegue otro.
func (r *urgentRig) expectSent(t *testing.T, n int, why string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for r.sentCount() < n && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(30 * time.Millisecond)
	if got := r.sentCount(); got != n {
		t.Fatalf("%s: %d envíos, se esperaban %d", why, got, n)
	}
}

func (r *urgentRig) sentCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.sent)
}

func (r *urgentRig) text(i int) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sent[i]
}

func TestUrgentFire_TextHasUrgentTriggerDetailAndGuide(t *testing.T) {
	r := newUrgentRig(t, 10*time.Minute)
	r.a.Fire(context.Background(), TriggerNectarDown, "5 fallos seguidos")
	r.expectSent(t, 1, "un aviso")
	text := r.text(0)
	for _, want := range []string{"URGENTE", "volver a Bird", string(TriggerNectarDown), "5 fallos seguidos", "docs/GUIA-VOLVER-A-BIRD.md"} {
		if !strings.Contains(text, want) {
			t.Errorf("el aviso no contiene %q: %s", want, text)
		}
	}
}

func TestUrgentFire_CooldownPerTrigger(t *testing.T) {
	r := newUrgentRig(t, 10*time.Minute)
	ctx := context.Background()
	r.a.Fire(ctx, TriggerProactiveStopped, "a")
	r.advance(5 * time.Minute)
	r.a.Fire(ctx, TriggerProactiveStopped, "b")
	r.expectSent(t, 1, "dentro del enfriamiento")
	// Otro disparador no queda bloqueado por el primero.
	r.a.Fire(ctx, TriggerEscalationFailing, "c")
	r.expectSent(t, 2, "disparadores distintos no se bloquean")
	r.advance(5 * time.Minute)
	r.a.Fire(ctx, TriggerProactiveStopped, "d")
	r.expectSent(t, 3, "tras el enfriamiento repite")
}

func TestUrgentCount_ThresholdInWindow(t *testing.T) {
	r := newUrgentRig(t, 10*time.Minute)
	ctx := context.Background()
	r.a.Count(ctx, TriggerNectarDown, 3, 5*time.Minute, "x")
	r.advance(time.Minute)
	r.a.Count(ctx, TriggerNectarDown, 3, 5*time.Minute, "x")
	r.expectSent(t, 0, "2 de 3")
	r.advance(time.Minute)
	r.a.Count(ctx, TriggerNectarDown, 3, 5*time.Minute, "último: HTTP 502")
	r.expectSent(t, 1, "3 de 3")
	if text := r.text(0); !strings.Contains(text, "último: HTTP 502") || !strings.Contains(text, "3 en menos de 5m0s (desde las 09:00:00)") {
		t.Errorf("el aviso dice cuántos, en qué ventana, desde cuándo y el detalle: %s", text)
	}
}

func TestUrgentCount_OldHitsOutsideWindowDoNotCount(t *testing.T) {
	r := newUrgentRig(t, time.Minute)
	ctx := context.Background()
	r.a.Count(ctx, TriggerEscalationFailing, 3, 5*time.Minute, "x")
	r.a.Count(ctx, TriggerEscalationFailing, 3, 5*time.Minute, "x")
	r.advance(6 * time.Minute)
	r.a.Count(ctx, TriggerEscalationFailing, 3, 5*time.Minute, "x")
	r.expectSent(t, 0, "las ocurrencias viejas no cuentan")
	r.a.Count(ctx, TriggerEscalationFailing, 3, 5*time.Minute, "x")
	r.a.Count(ctx, TriggerEscalationFailing, 3, 5*time.Minute, "x")
	r.expectSent(t, 1, "3 dentro de la ventana")
}

// Una caída sostenida no hace crecer la ventana: solo se guardan las últimas threshold ocurrencias.
func TestUrgentCount_WindowIsBounded(t *testing.T) {
	r := newUrgentRig(t, time.Hour)
	for i := 0; i < 500; i++ {
		r.a.Count(context.Background(), TriggerNectarDown, 5, time.Hour, "x")
	}
	r.a.mu.Lock()
	n := len(r.a.hits[TriggerNectarDown])
	r.a.mu.Unlock()
	if n > 5 {
		t.Fatalf("la ventana guarda %d ocurrencias, tope 5", n)
	}
	r.expectSent(t, 1, "un aviso dentro del enfriamiento")
}

func TestUrgentAlerter_NilReceiverIsNoop(_ *testing.T) {
	var a *UrgentAlerter
	a.Fire(context.Background(), TriggerNectarDown, "x")
	a.Count(context.Background(), TriggerNectarDown, 1, time.Minute, "x")
	a.Start(context.Background())
}

// Fire no bloquea a quien lo llama aunque Telegram no conteste: el envío es de Start, en su propia
// goroutine y con su propio timeout. El contexto cancelado de quien dispara no se lleva el aviso.
func TestUrgentFire_NeverBlocksAndSurvivesCanceledContext(t *testing.T) {
	release := make(chan struct{})
	delivered := make(chan error, 1)
	a := NewUrgentAlerter(func(ctx context.Context, _ string) error {
		<-release
		delivered <- ctx.Err()
		return nil
	}, "docs/GUIA-VOLVER-A-BIRD.md", time.Minute, time.Now)
	startAlerter(t, a)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	a.Fire(ctx, TriggerNectarDown, "x")
	a.Fire(context.Background(), TriggerEscalationFailing, "y") // el segundo espera en la cola
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Fatalf("Fire bloqueó %s con Telegram colgado", d)
	}
	close(release)
	select {
	case err := <-delivered:
		if err != nil {
			t.Fatalf("el envío no hereda la cancelación de quien dispara: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("el aviso no salió")
	}
}

// Concurrencia: Count y Fire desde varias goroutines (go test -race).
func TestUrgentAlerter_Concurrent(t *testing.T) {
	r := newUrgentRig(t, time.Hour)
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
	r.expectSent(t, 2, "un aviso por disparador dentro del enfriamiento")
}
