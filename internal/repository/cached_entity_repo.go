package repository

import (
	"context"
	"sync"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/domain"
)

// CachedEntityRepo wraps an EntityRepository with in-memory TTL cache
// for FindActive, FindActiveByCategory and FindByCode (M6, auditoría de queries: al agendar se
// consulta el contrato POR CADA CUP del grupo y los contratos son casi estáticos).
// GetCodeByIndexAndCategory se delega directo al inner repo.
type CachedEntityRepo struct {
	inner EntityRepository
	ttl   time.Duration

	mu         sync.RWMutex
	all        []domain.Entity
	byCategory map[string][]domain.Entity
	loadedAt   time.Time
	byCode     map[string]entityCodeEntry
}

type entityCodeEntry struct {
	at time.Time
	e  *domain.Entity
}

func NewCachedEntityRepo(inner EntityRepository, ttl time.Duration) *CachedEntityRepo {
	return &CachedEntityRepo{
		inner:      inner,
		ttl:        ttl,
		byCategory: make(map[string][]domain.Entity),
		byCode:     make(map[string]entityCodeEntry),
	}
}

func (c *CachedEntityRepo) isStale() bool {
	return c.loadedAt.IsZero() || time.Since(c.loadedAt) > c.ttl
}

func (c *CachedEntityRepo) refresh(ctx context.Context) error {
	all, err := c.inner.FindActive(ctx)
	if err != nil {
		return err
	}
	byCategory := make(map[string][]domain.Entity)
	for _, e := range all {
		byCategory[e.Category] = append(byCategory[e.Category], e)
	}
	c.all = all
	c.byCategory = byCategory
	c.loadedAt = time.Now()
	return nil
}

func (c *CachedEntityRepo) FindActive(ctx context.Context) ([]domain.Entity, error) {
	c.mu.RLock()
	if !c.isStale() {
		defer c.mu.RUnlock()
		return c.all, nil
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.isStale() {
		return c.all, nil
	}
	if err := c.refresh(ctx); err != nil {
		return nil, err
	}
	return c.all, nil
}

func (c *CachedEntityRepo) FindActiveByCategory(ctx context.Context, category string) ([]domain.Entity, error) {
	// Delegamos al inner repo directamente: cada categoría usa filtros SQL específicos
	// (es_prepagado, TipoAseguramiento) que no se pueden derivar solo del campo regimen
	// que usa el caché global. El caché byCategory queda solo para compatibilidad.
	return c.inner.FindActiveByCategory(ctx, category)
}

func (c *CachedEntityRepo) FindByCode(ctx context.Context, code string) (*domain.Entity, error) {
	c.mu.RLock()
	if entry, ok := c.byCode[code]; ok && time.Since(entry.at) <= c.ttl {
		c.mu.RUnlock()
		return entry.e, nil
	}
	c.mu.RUnlock()

	e, err := c.inner.FindByCode(ctx, code)
	if err != nil {
		return nil, err // los errores no se cachean
	}
	c.mu.Lock()
	c.byCode[code] = entityCodeEntry{at: time.Now(), e: e}
	c.mu.Unlock()
	return e, nil
}

func (c *CachedEntityRepo) GetCodeByIndexAndCategory(ctx context.Context, index int, category string) (string, error) {
	return c.inner.GetCodeByIndexAndCategory(ctx, index, category)
}
