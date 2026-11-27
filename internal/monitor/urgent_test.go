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
	if text := r.text(0); !strings.Contains(text, "último: HTTP 502") || !strings.Contains(text, "al menos 3 en menos de 5 minutos (desde las 09:00:00)") {
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

// El texto de Count no promete un conteo exacto (solo se guardan las últimas threshold ocurrencias)
// y da la ventana en minutos enteros, para quien no programa.
func TestUrgentCount_TextSaysAtLeastAndWholeMinutes(t *testing.T) {
	for _, tc := range []struct {
		window time.Duration
		want   string
	}{
		{5 * time.Minute, "al menos 2 en menos de 5 minutos (desde las 09:00:00)"},
		{time.Minute, "al menos 2 en menos de 1 minuto (desde las 09:00:00)"},
		{time.Hour, "al menos 2 en menos de 60 minutos (desde las 09:00:00)"},
	} {
		r := newUrgentRig(t, time.Hour)
		for i := 0; i < 5; i++ { // más ocurrencias que el umbral: el número es un mínimo
			r.a.Count(context.Background(), TriggerNectarDown, 2, tc.window, "último: HTTP 503")
		}
		r.expectSent(t, 1, "un aviso")
		if text := r.text(0); !strings.Contains(text, tc.want) || strings.Contains(text, "m0s") {
			t.Errorf("ventana %s: el aviso debe decir %q: %s", tc.window, tc.want, text)
		}
	}
}

// Si la cola está llena el aviso se descarta, pero sin gastar el enfriamiento: el siguiente Fire
// del mismo disparador sale en cuanto haya sitio, no al cabo de cooldown.
func TestUrgentFire_QueueFullDoesNotSpendCooldown(t *testing.T) {
	clock := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	a := NewUrgentAlerter(func(context.Context, string) error { return nil },
		"docs/GUIA-VOLVER-A-BIRD.md", time.Hour, func() time.Time { return clock })
	// Sin Start: la cola se llena con avisos de otro disparador, uno por enfriamiento.
	for i := 0; i < urgentQueueSize; i++ {
		a.Fire(context.Background(), TriggerNectarDown, "x")
		clock = clock.Add(time.Hour)
	}
	a.Fire(context.Background(), TriggerProactiveStopped, "descartado")
	if n := len(a.queue); n != urgentQueueSize {
		t.Fatalf("la cola debía estar llena: %d", n)
	}
	<-a.queue // se libera un sitio, dentro del mismo enfriamiento
	a.Fire(context.Background(), TriggerProactiveStopped, "reintento")
	if n := len(a.queue); n != urgentQueueSize {
		t.Fatalf("el aviso descartado no debe gastar el enfriamiento: cola=%d", n)
	}
	var last urgentMessage
	for len(a.queue) > 0 {
		last = <-a.queue
	}
	if last.trigger != TriggerProactiveStopped || !strings.Contains(last.text, "reintento") {
		t.Fatalf("el último encolado debe ser el reintento: %+v", last)
	}
}

// Con la línea vieja armada, volver a Bird ya no devuelve ninguna línea de WhatsApp: el aviso pide el
// rescate a los supervisores de Néctar y no menciona Bird.
func TestUrgent_LegacyArmedTextDoesNotSayBird(t *testing.T) {
	r := newUrgentRig(t, time.Hour)
	r.a.SetLegacyLineArmed(true)
	r.a.Fire(context.Background(), TriggerNectarDown, "5 fallos seguidos")
	r.expectSent(t, 1, "el aviso sale igual")
	got := r.text(0)
	if strings.Contains(got, "volver a Bird") || strings.Contains(got, "GUIA-VOLVER-A-BIRD") {
		t.Fatalf("no debe mandar volver a Bird: %q", got)
	}
	if !strings.Contains(got, "Pide a un supervisor de Néctar que rescate las conversaciones del bot (motivo bot_down).") {
		t.Fatalf("falta la instrucción de rescate: %q", got)
	}
	if !strings.Contains(got, string(TriggerNectarDown)) || !strings.Contains(got, "5 fallos seguidos") {
		t.Fatalf("faltan disparador o detalle: %q", got)
	}
}
