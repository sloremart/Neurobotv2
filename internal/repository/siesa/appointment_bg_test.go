package siesa

import (
	"testing"
	"time"
)

// TestWaitBackground_WaitsForInflight verifica el #33: WaitBackground bloquea hasta que terminan las
// auditorías fire-and-forget en vuelo (bgWG), para que el apagado no cierre la BD mientras escriben.
func TestWaitBackground_WaitsForInflight(t *testing.T) {
	r := &AppointmentRepo{}
	r.bgWG.Add(1) // simula una auditoría en vuelo

	done := make(chan struct{})
	go func() {
		r.WaitBackground(2 * time.Second)
		close(done)
	}()

	select {
	case <-done:
		t.Fatal("WaitBackground retornó antes de que terminara la goroutine en vuelo")
	case <-time.After(50 * time.Millisecond):
	}

	r.bgWG.Done() // la auditoría termina

	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatal("WaitBackground no retornó tras terminar la goroutine")
	}
}

// TestWaitBackground_TimesOut: si una auditoría no termina, WaitBackground retorna al vencer el timeout
// (best-effort) en vez de colgar el apagado indefinidamente.
func TestWaitBackground_TimesOut(t *testing.T) {
	r := &AppointmentRepo{}
	r.bgWG.Add(1) // nunca hace Done → debe vencer el timeout

	start := time.Now()
	r.WaitBackground(100 * time.Millisecond)
	elapsed := time.Since(start)

	if elapsed < 100*time.Millisecond || elapsed > 1*time.Second {
		t.Fatalf("esperaba retorno por timeout ~100ms, got %v", elapsed)
	}
	r.bgWG.Done() // limpiar la goroutine de espera interna
}
