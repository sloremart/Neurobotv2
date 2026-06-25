package session

import (
	"context"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPhoneMutex_LockAcquires(t *testing.T) {
	pm := NewPhoneMutex()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := pm.Lock(ctx, "+573001234567")
	if err != nil {
		t.Fatal(err)
	}
	pm.Unlock("+573001234567")
}

func TestPhoneMutex_SamePhoneBlocks(t *testing.T) {
	pm := NewPhoneMutex()
	ctx := context.Background()

	// Acquire lock
	err := pm.Lock(ctx, "+573001234567")
	if err != nil {
		t.Fatal(err)
	}

	// Try to acquire same phone with short timeout
	shortCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()

	err = pm.Lock(shortCtx, "+573001234567")
	if err == nil {
		t.Error("expected timeout error when same phone is locked")
	}

	pm.Unlock("+573001234567")
}

func TestPhoneMutex_ContextCancel(t *testing.T) {
	pm := NewPhoneMutex()
	ctx := context.Background()

	// Acquire first
	pm.Lock(ctx, "+573001234567")

	// Cancel context
	cancelCtx, cancel := context.WithCancel(ctx)
	cancel() // cancel immediately

	err := pm.Lock(cancelCtx, "+573001234567")
	if err == nil {
		t.Error("expected error on cancelled context")
	}

	pm.Unlock("+573001234567")
}

func TestPhoneMutex_DifferentPhonesNoBlock(t *testing.T) {
	pm := NewPhoneMutex()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err1 := pm.Lock(ctx, "+573001111111")
	if err1 != nil {
		t.Fatal(err1)
	}

	err2 := pm.Lock(ctx, "+573002222222")
	if err2 != nil {
		t.Fatal("different phones should not block each other")
	}

	pm.Unlock("+573001111111")
	pm.Unlock("+573002222222")
}

func TestPhoneMutex_ConcurrentSerialization(t *testing.T) {
	pm := NewPhoneMutex()
	phone := "+573001234567"
	counter := 0
	var mu sync.Mutex

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			if err := pm.Lock(ctx, phone); err != nil {
				return
			}
			defer pm.Unlock(phone)

			mu.Lock()
			counter++
			mu.Unlock()

			time.Sleep(5 * time.Millisecond)
		}()
	}

	wg.Wait()

	mu.Lock()
	if counter != 5 {
		t.Errorf("expected all 5 goroutines to complete, got %d", counter)
	}
	mu.Unlock()
}

func TestPhoneMutex_CleanupRemovesOldLocks(t *testing.T) {
	pm := NewPhoneMutex()
	ctx := context.Background()

	// Acquire and release a lock
	phone := "+573001234567"
	if err := pm.Lock(ctx, phone); err != nil {
		t.Fatal(err)
	}
	pm.Unlock(phone)

	// Manually set lastUsed to 15 minutes ago so cleanup will remove it
	pm.mu.Lock()
	pl, ok := pm.locks[phone]
	if !ok {
		pm.mu.Unlock()
		t.Fatal("expected lock to exist after unlock")
	}
	pl.lastUsed = time.Now().Add(-15 * time.Minute)
	pm.mu.Unlock()

	pm.cleanupIdle(time.Now().Add(-10 * time.Minute))

	// Lock should have been cleaned up
	pm.mu.Lock()
	_, exists := pm.locks[phone]
	pm.mu.Unlock()
	if exists {
		t.Error("expected old lock to be removed by cleanup")
	}
}

func TestPhoneMutex_CleanupKeepsActiveLocks(t *testing.T) {
	pm := NewPhoneMutex()
	ctx := context.Background()

	phone := "+573001234567"
	if err := pm.Lock(ctx, phone); err != nil {
		t.Fatal(err)
	}
	// Don't unlock — lock is active (refCount > 0)

	// Set lastUsed to old time
	pm.mu.Lock()
	pl := pm.locks[phone]
	pl.lastUsed = time.Now().Add(-15 * time.Minute)
	pm.mu.Unlock()

	// Cleanup should NOT remove it because refCount > 0
	pm.cleanupIdle(time.Now().Add(-10 * time.Minute))

	pm.mu.Lock()
	_, exists := pm.locks[phone]
	pm.mu.Unlock()
	if !exists {
		t.Error("active lock should NOT be removed by cleanup")
	}

	pm.Unlock(phone)
}

// TestPhoneMutex_NoGoroutineLeakOnTimeout cubre N-40: un Lock que expira NO debe dejar una
// goroutine huérfana. El diseño anterior creaba una goroutine por Lock que quedaba bloqueada
// hasta 60s tras el timeout; el nuevo (semáforo) no crea ninguna.
func TestPhoneMutex_NoGoroutineLeakOnTimeout(t *testing.T) {
	pm := NewPhoneMutex()
	phone := "+573001234567"

	// Sostener el lock para que todos los intentos siguientes expiren.
	if err := pm.Lock(context.Background(), phone); err != nil {
		t.Fatal(err)
	}
	defer pm.Unlock(phone)

	runtime.GC()
	before := runtime.NumGoroutine()

	const attempts = 200
	for i := 0; i < attempts; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
		err := pm.Lock(ctx, phone)
		cancel()
		if err == nil {
			t.Fatal("expected timeout while phone is held")
		}
	}

	time.Sleep(50 * time.Millisecond) // dar margen por si (mal) se spawnearon goroutines
	runtime.GC()
	after := runtime.NumGoroutine()

	// El diseño viejo fugaba ~1 goroutine por intento (viva hasta 60s). El nuevo, ninguna;
	// se permite un pequeño margen por ruido del runtime.
	if after-before > 10 {
		t.Errorf("goroutine leak on timeout: before=%d after=%d (delta=%d, attempts=%d)",
			before, after, after-before, attempts)
	}
}

// TestPhoneMutex_ExclusionSurvivesTimedOutWaiter cubre N-18 (determinista): tras un waiter que
// expira, la exclusión mutua debe SEGUIR intacta (no debe crearse un lock fresco que permita
// concurrencia), y al liberar el holder, otro debe poder adquirir.
func TestPhoneMutex_ExclusionSurvivesTimedOutWaiter(t *testing.T) {
	pm := NewPhoneMutex()
	phone := "+573001234567"

	// A adquiere.
	if err := pm.Lock(context.Background(), phone); err != nil {
		t.Fatal(err)
	}

	// B intenta y expira.
	ctxB, cancelB := context.WithTimeout(context.Background(), 10*time.Millisecond)
	errB := pm.Lock(ctxB, phone)
	cancelB()
	if errB == nil {
		t.Fatal("B debió expirar mientras A sostiene el lock")
	}

	// Con A aún sosteniendo, C también debe expirar — la exclusión debe sobrevivir al waiter
	// que expiró (en el bug N-18 podía quedar un lock huérfano y C adquirir un lock fresco).
	ctxC, cancelC := context.WithTimeout(context.Background(), 10*time.Millisecond)
	errC := pm.Lock(ctxC, phone)
	cancelC()
	if errC == nil {
		t.Fatal("C debió expirar: A sigue sosteniendo, la exclusión debe sobrevivir (N-18)")
	}

	// A libera; ahora D adquiere de inmediato.
	pm.Unlock(phone)
	ctxD, cancelD := context.WithTimeout(context.Background(), time.Second)
	defer cancelD()
	if err := pm.Lock(ctxD, phone); err != nil {
		t.Fatalf("D debió adquirir tras liberar A: %v", err)
	}
	pm.Unlock(phone)
}

// TestPhoneMutex_MutualExclusionUnderTimeoutChurn estresa el lock con muchas goroutines que
// hacen Lock (con timeout corto) / Unlock sobre el mismo teléfono. Un contador de "activos"
// nunca debe pasar de 1. Correr con -race valida además accesos a campos.
func TestPhoneMutex_MutualExclusionUnderTimeoutChurn(t *testing.T) {
	pm := NewPhoneMutex()
	phone := "+573001234567"

	var active int32
	var maxSeen int32
	var wg sync.WaitGroup

	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
				err := pm.Lock(ctx, phone)
				cancel()
				if err != nil {
					continue // expiró; está bien
				}
				n := atomic.AddInt32(&active, 1)
				for {
					prev := atomic.LoadInt32(&maxSeen)
					if n <= prev || atomic.CompareAndSwapInt32(&maxSeen, prev, n) {
						break
					}
				}
				time.Sleep(time.Millisecond) // sostener un instante para provocar contención
				atomic.AddInt32(&active, -1)
				pm.Unlock(phone)
			}
		}()
	}
	wg.Wait()

	if m := atomic.LoadInt32(&maxSeen); m > 1 {
		t.Errorf("exclusión mutua violada: %d goroutines sostuvieron el mismo lock a la vez", m)
	}
}

func TestPhoneMutex_StartCleanup_ContextCancellation(t *testing.T) {
	pm := NewPhoneMutex()
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		pm.StartCleanup(ctx)
		close(done)
	}()

	// Cancel immediately
	cancel()

	// Should return quickly
	select {
	case <-done:
		// OK
	case <-time.After(2 * time.Second):
		t.Fatal("StartCleanup did not exit after context cancellation")
	}
}
