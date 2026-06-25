package session

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// phoneLock es la entrada por-teléfono. El candado real es un semáforo: un canal con buffer 1
// que contiene un token mientras el teléfono está bloqueado (lleno = locked, vacío = libre).
// refCount y lastUsed se leen/escriben SIEMPRE bajo PhoneMutex.mu.
type phoneLock struct {
	sem      chan struct{} // buffer 1: semáforo de exclusión mutua
	refCount int           // nº de Lock en vuelo (esperando o sosteniendo)
	lastUsed time.Time     // último uso, para el GC de inactivos
}

// PhoneMutex serializa el procesamiento por número de teléfono: eventos del MISMO teléfono se
// atienden uno a la vez; teléfonos distintos corren en paralelo.
//
// Implementación (N-18/N-40): un semáforo por teléfono (canal buffer 1). Lock envía un token o
// espera a que el ctx expire; Unlock lo recibe. NO se lanza ninguna goroutine por Lock. El
// diseño anterior creaba una goroutine que se bloqueaba en sync.Mutex y, al expirar el timeout,
// quedaba huérfana reteniendo el mutex para siempre (→ el entry se borraba y el siguiente Lock
// creaba uno fresco → dos mensajes del mismo teléfono en paralelo). Aquí un Lock que expira
// simplemente no envió el token: no hay nada que abandonar. Además refCount vive bajo `mu`, así
// que el cleanup nunca puede borrar un lock que otra goroutine acaba de tomar.
type PhoneMutex struct {
	mu    sync.Mutex
	locks map[string]*phoneLock
}

func NewPhoneMutex() *PhoneMutex {
	return &PhoneMutex{locks: make(map[string]*phoneLock)}
}

// getOrCreate devuelve el phoneLock del teléfono e incrementa su refCount, todo bajo mu.
func (pm *PhoneMutex) getOrCreate(phone string) *phoneLock {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pl := pm.locks[phone]
	if pl == nil {
		pl = &phoneLock{sem: make(chan struct{}, 1)}
		pm.locks[phone] = pl
	}
	pl.refCount++
	return pl
}

// Lock adquiere el lock para un teléfono respetando el timeout/cancelación del ctx.
// Si el ctx expira antes de adquirir, devuelve error SIN dejar el teléfono bloqueado.
func (pm *PhoneMutex) Lock(ctx context.Context, phone string) error {
	pl := pm.getOrCreate(phone)

	select {
	case pl.sem <- struct{}{}: // token enviado → adquirido
		pm.mu.Lock()
		pl.lastUsed = time.Now()
		pm.mu.Unlock()
		return nil
	case <-ctx.Done(): // timeout/cancel: no enviamos token, nada que liberar
		pm.mu.Lock()
		pl.refCount--
		pm.mu.Unlock()
		return fmt.Errorf("phone lock timeout for %s: %w", phone, ctx.Err())
	}
}

// Unlock libera el lock para un teléfono. Debe llamarse solo tras un Lock exitoso.
func (pm *PhoneMutex) Unlock(phone string) {
	pm.mu.Lock()
	pl := pm.locks[phone]
	if pl != nil {
		pl.refCount--
		pl.lastUsed = time.Now()
	}
	pm.mu.Unlock()
	if pl != nil {
		select {
		case <-pl.sem: // recibe el token → libera el semáforo
		default: // no estaba locked (defensivo; no debería ocurrir con uso correcto)
		}
	}
}

// cleanupIdle borra las entradas con refCount==0 e inactivas antes de `threshold`.
// Devuelve cuántas borró. Aislado para poder testearlo sin el ticker.
func (pm *PhoneMutex) cleanupIdle(threshold time.Time) int {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	cleaned := 0
	for key, pl := range pm.locks {
		if pl.refCount == 0 && pl.lastUsed.Before(threshold) {
			delete(pm.locks, key)
			cleaned++
		}
	}
	return cleaned
}

// StartCleanup borra entradas inactivas (cada 5 min, refCount==0 e inactivas >10 min).
func (pm *PhoneMutex) StartCleanup(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if cleaned := pm.cleanupIdle(time.Now().Add(-10 * time.Minute)); cleaned > 0 {
				slog.Debug("phone locks cleaned", "count", cleaned)
			}
		}
	}
}
