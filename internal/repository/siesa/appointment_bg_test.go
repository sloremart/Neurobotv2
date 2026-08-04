package siesa

import (
	"sync"
	"sync/atomic"
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

// Auditoría queries M8: cada Confirm/Cancel/Create lanza una goroutine de auditoría; sin tope, un
// pico de confirmaciones compite por las 10 conexiones del pool contra la ruta caliente. El
// semáforo acota cuántas escrituras de auditoría corren a la vez (las demás esperan su turno,
// ninguna se pierde).
func TestAuditSemaphoreBoundsConcurrency(t *testing.T) {
	r := NewAppointmentRepo(nil, "", 0)
	var cur, peak atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release := r.acquireAuditSlot()
			defer release()
			c := cur.Add(1)
			for {
				p := peak.Load()
				if c <= p || peak.CompareAndSwap(p, c) {
					break
				}
			}
			time.Sleep(10 * time.Millisecond)
			cur.Add(-1)
		}()
	}
	wg.Wait()
	if got := peak.Load(); got > maxConcurrentAudits {
		t.Errorf("concurrencia pico = %d, tope = %d", got, maxConcurrentAudits)
	}
}

// El semáforo es nil-safe: un repo construido a pelo (tests) no debe bloquearse ni panickear.
func TestAuditSemaphoreNilSafe(_ *testing.T) {
	r := &AppointmentRepo{}
	release := r.acquireAuditSlot()
	release()
}
