package repository

import (
	"context"
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/domain"
)

// Auditoría queries M6: al agendar se llama FindByCode y FindPrice POR CADA CUP del grupo, sin
// caché, aunque contratos y tarifas son casi estáticos. Ambos lookups se cachean con TTL.

type countingEntityRepo struct {
	calls int
}

func (f *countingEntityRepo) FindActive(context.Context) ([]domain.Entity, error) { return nil, nil }
func (f *countingEntityRepo) FindActiveByCategory(context.Context, string) ([]domain.Entity, error) {
	return nil, nil
}

func (f *countingEntityRepo) FindByCode(_ context.Context, code string) (*domain.Entity, error) {
	f.calls++
	return &domain.Entity{Code: code}, nil
}

func (f *countingEntityRepo) GetCodeByIndexAndCategory(context.Context, int, string) (string, error) {
	return "", nil
}

func TestCachedEntityRepo_FindByCodeUsesTTLCache(t *testing.T) {
	inner := &countingEntityRepo{}
	c := NewCachedEntityRepo(inner, time.Minute)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		e, err := c.FindByCode(ctx, "EPS005")
		if err != nil || e == nil || e.Code != "EPS005" {
			t.Fatalf("FindByCode: e=%v err=%v", e, err)
		}
	}
	if inner.calls != 1 {
		t.Errorf("inner FindByCode llamadas = %d, want 1 (cacheado)", inner.calls)
	}
	if _, err := c.FindByCode(ctx, "EPS002"); err != nil {
		t.Fatal(err)
	}
	if inner.calls != 2 {
		t.Errorf("inner FindByCode llamadas = %d, want 2 (código distinto)", inner.calls)
	}
}

type countingPriceRepo struct {
	calls int
}

func (f *countingPriceRepo) FindPrice(_ context.Context, _, _ string) (*float64, error) {
	f.calls++
	v := 1000.0
	return &v, nil
}

func TestCachedPriceRepo_UsesTTLCache(t *testing.T) {
	inner := &countingPriceRepo{}
	c := NewCachedPriceRepo(inner, time.Minute)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		p, err := c.FindPrice(ctx, "890274", "11")
		if err != nil || p == nil || *p != 1000.0 {
			t.Fatalf("FindPrice: p=%v err=%v", p, err)
		}
	}
	if inner.calls != 1 {
		t.Errorf("inner FindPrice llamadas = %d, want 1 (cacheado)", inner.calls)
	}
	if _, err := c.FindPrice(ctx, "890274", "32"); err != nil {
		t.Fatal(err)
	}
	if inner.calls != 2 {
		t.Errorf("inner FindPrice llamadas = %d, want 2 (manual distinto)", inner.calls)
	}
}
