package repository

import (
	"context"
	"sync"
	"time"
)

// CachedPriceRepo envuelve un PriceRepository con caché TTL por (cupCode, tariffType).
// M6 (auditoría de queries): al agendar se cotiza CADA CUP del grupo contra sis_proc_precios
// (~1.5M filas) sin caché, y las tarifas solo cambian con actualizaciones de manual (raras).
// Cachea también el nil (= CUPS no cubierto): la cobertura tampoco cambia minuto a minuto.
type CachedPriceRepo struct {
	inner PriceRepository
	ttl   time.Duration

	mu    sync.RWMutex
	cache map[string]priceEntry
}

type priceEntry struct {
	at    time.Time
	price *float64
}

// NewCachedPriceRepo crea el caché de tarifas con el TTL dado.
func NewCachedPriceRepo(inner PriceRepository, ttl time.Duration) *CachedPriceRepo {
	return &CachedPriceRepo{inner: inner, ttl: ttl, cache: make(map[string]priceEntry)}
}

var _ PriceRepository = (*CachedPriceRepo)(nil)

// FindPrice devuelve el precio cacheado o consulta al repo interno. Los errores no se cachean.
func (c *CachedPriceRepo) FindPrice(ctx context.Context, cupCode, tariffType string) (*float64, error) {
	key := cupCode + "|" + tariffType
	c.mu.RLock()
	if e, ok := c.cache[key]; ok && time.Since(e.at) <= c.ttl {
		c.mu.RUnlock()
		return e.price, nil
	}
	c.mu.RUnlock()

	p, err := c.inner.FindPrice(ctx, cupCode, tariffType)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	// Purga oportunista de entradas vencidas para que el mapa no crezca sin tope.
	for k, e := range c.cache {
		if time.Since(e.at) > c.ttl {
			delete(c.cache, k)
		}
	}
	c.cache[key] = priceEntry{at: time.Now(), price: p}
	c.mu.Unlock()
	return p, nil
}
