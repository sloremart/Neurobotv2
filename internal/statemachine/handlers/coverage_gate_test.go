package handlers

import (
	"context"
	"fmt"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/repository"
	"github.com/neuro-bot/neuro-bot/internal/session"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
	"github.com/neuro-bot/neuro-bot/internal/testutil"
)

// fakePriceRepo implementa repository.PriceRepository devolviendo *float64 (el mock de testutil
// devuelve float64 y no satisface la interfaz; por eso uno local para el gate de cobertura).
type fakePriceRepo struct {
	fn func(ctx context.Context, cup, manual string) (*float64, error)
}

func (f *fakePriceRepo) FindPrice(ctx context.Context, cup, manual string) (*float64, error) {
	return f.fn(ctx, cup, manual)
}

var _ repository.PriceRepository = (*fakePriceRepo)(nil)

func sessWithContract(contract string) *session.Session {
	s := testSess(sm.StateSearchSlots)
	if contract != "" {
		s.Context["patient_contract"] = contract
	}
	return s
}

func TestIsCupCovered(t *testing.T) {
	ctx := context.Background()
	ptr := func(v float64) *float64 { return &v }
	ent := &testutil.MockEntityRepo{
		FindByCodeFn: func(_ context.Context, _ string) (*domain.Entity, error) {
			return &domain.Entity{PriceType: "11"}, nil
		},
	}

	t.Run("cubierto: precio>0", func(t *testing.T) {
		p := &fakePriceRepo{fn: func(_ context.Context, _, _ string) (*float64, error) { return ptr(57649), nil }}
		covered, err := isCupCovered(ctx, p, ent, sessWithContract("4"), "890274")
		if err != nil || !covered {
			t.Fatalf("esperaba cubierto; got covered=%v err=%v", covered, err)
		}
	})
	t.Run("sin convenio: precio 0", func(t *testing.T) {
		p := &fakePriceRepo{fn: func(_ context.Context, _, _ string) (*float64, error) { return ptr(0), nil }}
		covered, err := isCupCovered(ctx, p, ent, sessWithContract("4"), "890274")
		if err != nil || covered {
			t.Fatalf("esperaba NO cubierto; got covered=%v err=%v", covered, err)
		}
	})
	t.Run("sin convenio: precio nil", func(t *testing.T) {
		p := &fakePriceRepo{fn: func(_ context.Context, _, _ string) (*float64, error) { return nil, nil }}
		covered, err := isCupCovered(ctx, p, ent, sessWithContract("4"), "890274")
		if err != nil || covered {
			t.Fatalf("esperaba NO cubierto; got covered=%v err=%v", covered, err)
		}
	})
	t.Run("error en findprice -> error (fail-open en caller)", func(t *testing.T) {
		p := &fakePriceRepo{fn: func(_ context.Context, _, _ string) (*float64, error) { return nil, fmt.Errorf("db caída") }}
		if _, err := isCupCovered(ctx, p, ent, sessWithContract("4"), "890274"); err == nil {
			t.Fatal("esperaba error para que el caller haga fail-open")
		}
	})
	t.Run("sin contrato/entidad -> error", func(t *testing.T) {
		p := &fakePriceRepo{fn: func(_ context.Context, _, _ string) (*float64, error) { return ptr(1), nil }}
		if _, err := isCupCovered(ctx, p, ent, sessWithContract(""), "890274"); err == nil {
			t.Fatal("esperaba error sin contrato/entidad")
		}
	})
}

// TestAnyCupCovered (L1): el gate debe cubrir si CUALQUIER código del grupo tiene convenio.
func TestAnyCupCovered(t *testing.T) {
	ctx := context.Background()
	ptr := func(v float64) *float64 { return &v }
	ent := &testutil.MockEntityRepo{
		FindByCodeFn: func(_ context.Context, _ string) (*domain.Entity, error) {
			return &domain.Entity{PriceType: "11"}, nil
		},
	}
	codes := []string{"EMG", "NC"} // primario + alternativo

	t.Run("primario sin convenio pero alternativo cubierto -> cubierto", func(t *testing.T) {
		p := &fakePriceRepo{fn: func(_ context.Context, cup, _ string) (*float64, error) {
			if cup == "NC" {
				return ptr(50000), nil
			}
			return ptr(0), nil // EMG primario sin convenio
		}}
		covered, err := anyCupCovered(ctx, p, ent, sessWithContract("4"), codes)
		if err != nil || !covered {
			t.Fatalf("esperaba cubierto por el alternativo; got covered=%v err=%v", covered, err)
		}
	})
	t.Run("ninguno cubierto -> no cubierto", func(t *testing.T) {
		p := &fakePriceRepo{fn: func(_ context.Context, _, _ string) (*float64, error) { return ptr(0), nil }}
		covered, err := anyCupCovered(ctx, p, ent, sessWithContract("4"), codes)
		if err != nil || covered {
			t.Fatalf("esperaba NO cubierto; got covered=%v err=%v", covered, err)
		}
	})
	t.Run("primario error + alternativo cubierto -> cubierto (no bloquea)", func(t *testing.T) {
		p := &fakePriceRepo{fn: func(_ context.Context, cup, _ string) (*float64, error) {
			if cup == "NC" {
				return ptr(50000), nil
			}
			return nil, fmt.Errorf("db blip")
		}}
		covered, err := anyCupCovered(ctx, p, ent, sessWithContract("4"), codes)
		if err != nil || !covered {
			t.Fatalf("esperaba cubierto; got covered=%v err=%v", covered, err)
		}
	})
	t.Run("todos error -> error (fail-open en el caller)", func(t *testing.T) {
		p := &fakePriceRepo{fn: func(_ context.Context, _, _ string) (*float64, error) { return nil, fmt.Errorf("db caída") }}
		if covered, err := anyCupCovered(ctx, p, ent, sessWithContract("4"), codes); err == nil || covered {
			t.Fatalf("esperaba (false,err) para fail-open; got covered=%v err=%v", covered, err)
		}
	})
}

func TestCoverageNoConvenioHandler(t *testing.T) {
	ctx := context.Background()
	h := coverageNoConvenioHandler()

	t.Run("particular -> SEARCH_SLOTS + PART02 + limpia contrato", func(t *testing.T) {
		res, err := h(ctx, testSess(sm.StateCoverageNoConvenio), postbackM("cov_particular"))
		if err != nil {
			t.Fatal(err)
		}
		if res.NextState != sm.StateSearchSlots {
			t.Errorf("esperaba SEARCH_SLOTS, got %s", res.NextState)
		}
		if res.UpdateCtx["patient_entity"] != particularEntityCode {
			t.Errorf("esperaba patient_entity=%s, got %q", particularEntityCode, res.UpdateCtx["patient_entity"])
		}
		cleared := false
		for _, k := range res.ClearCtx {
			if k == "patient_contract" {
				cleared = true
			}
		}
		if !cleared {
			t.Error("esperaba limpiar patient_contract")
		}
	})
	t.Run("agente -> ESCALATE_TO_AGENT", func(t *testing.T) {
		res, err := h(ctx, testSess(sm.StateCoverageNoConvenio), postbackM("cov_agente"))
		if err != nil {
			t.Fatal(err)
		}
		if res.NextState != sm.StateEscalateToAgent {
			t.Errorf("esperaba ESCALATE_TO_AGENT, got %s", res.NextState)
		}
	})
}
