package repository

import (
	"context"
	"sync"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/domain"
)

// CachedProcedureRepo wraps a ProcedureRepository with in-memory TTL cache
// for FindAllActive. Individual lookups (FindByCode, FindByID, SearchByName)
// are delegated directly to the inner repo.
type CachedProcedureRepo struct {
	inner ProcedureRepository
	ttl   time.Duration

	mu       sync.RWMutex
	all      []domain.Procedure
	loadedAt time.Time
}

func NewCachedProcedureRepo(inner ProcedureRepository, ttl time.Duration) *CachedProcedureRepo {
	return &CachedProcedureRepo{inner: inner, ttl: ttl}
}

func (c *CachedProcedureRepo) FindAllActive(ctx context.Context) ([]domain.Procedure, error) {
	c.mu.RLock()
	if !c.loadedAt.IsZero() && time.Since(c.loadedAt) <= c.ttl {
		defer c.mu.RUnlock()
		return c.all, nil
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.loadedAt.IsZero() && time.Since(c.loadedAt) <= c.ttl {
		return c.all, nil
	}
	all, err := c.inner.FindAllActive(ctx)
	if err != nil {
		return nil, err
	}
	c.all = all
	c.loadedAt = time.Now()
	return c.all, nil
}

func (c *CachedProcedureRepo) FindByCode(ctx context.Context, code string) (*domain.Procedure, error) {
	return c.inner.FindByCode(ctx, code)
}

func (c *CachedProcedureRepo) FindByID(ctx context.Context, id int) (*domain.Procedure, error) {
	return c.inner.FindByID(ctx, id)
}

func (c *CachedProcedureRepo) SearchByName(ctx context.Context, name string) ([]domain.Procedure, error) {
	return c.inner.SearchByName(ctx, name)
}

func (c *CachedProcedureRepo) FindSubjectTypeForCups(ctx context.Context, cupsCode string) (int, error) {
	return c.inner.FindSubjectTypeForCups(ctx, cupsCode)
}

// FindMedicosForCups delega en el repo interno (sin caché; lookup individual).
func (c *CachedProcedureRepo) FindMedicosForCups(ctx context.Context, cupsCode string) ([]int, error) {
	return c.inner.FindMedicosForCups(ctx, cupsCode)
}
