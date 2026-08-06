package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/domain"
)

// KPI "recuperación de cupos cancelados": el endpoint entrega el agregado SIESA (slots con cita
// cancelada y cuántos se re-ocuparon) cruzado con la BD local (cuáles re-ocupaciones nacieron de
// la lista de espera). El dashboard lo proxea tal cual.
func TestHandleSiesaSlotRecovery(t *testing.T) {
	var gotDays int
	analytics := &mockSiesaAnalyticsReader{
		slotRecoveryFn: func(_ context.Context, days int) (domain.SlotRecoveryData, error) {
			gotDays = days
			return domain.SlotRecoveryData{
				PorDia: []domain.SlotRecoveryDay{
					{Dia: "2026-08-01", Canceladas: 10, Rellenadas: 6},
					{Dia: "2026-08-02", Canceladas: 5, Rellenadas: 2},
				},
				RefillCitaIDs: []string{"100", "101", "102"},
			}, nil
		},
	}
	var gotIDs []string
	wl := &mockWaitingListReader{
		countWLBookingsFn: func(_ context.Context, ids []string) (int, error) {
			gotIDs = ids
			return 2, nil
		},
	}
	h := &InternalHandler{siesaAnalytics: analytics, waitingListRepo: wl}

	req := httptest.NewRequest("GET", "/api/internal/siesa/slot-recovery?dias=30", nil)
	rec := httptest.NewRecorder()
	h.HandleSiesaSlotRecovery(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if gotDays != 30 {
		t.Errorf("esperaba days=30 hacia el repo, got %d", gotDays)
	}
	if len(gotIDs) != 3 {
		t.Errorf("el cruce WL debe recibir los 3 ids de re-ocupación, got %v", gotIDs)
	}
	var resp struct {
		Dias         int                      `json:"dias"`
		Canceladas   int                      `json:"canceladas"`
		Rellenadas   int                      `json:"rellenadas"`
		RellenadasWL int                      `json:"rellenadas_wl"`
		PorDia       []domain.SlotRecoveryDay `json:"por_dia"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Canceladas != 15 || resp.Rellenadas != 8 || resp.RellenadasWL != 2 {
		t.Errorf("totales: canceladas=%d rellenadas=%d wl=%d, want 15/8/2", resp.Canceladas, resp.Rellenadas, resp.RellenadasWL)
	}
	if len(resp.PorDia) != 2 || resp.Dias != 30 {
		t.Errorf("por_dia=%d dias=%d, want 2/30", len(resp.PorDia), resp.Dias)
	}
}

// Sin re-ocupaciones no se debe consultar el cruce WL (evita el IN vacío).
func TestHandleSiesaSlotRecovery_NoRefills(t *testing.T) {
	analytics := &mockSiesaAnalyticsReader{
		slotRecoveryFn: func(_ context.Context, _ int) (domain.SlotRecoveryData, error) {
			return domain.SlotRecoveryData{PorDia: []domain.SlotRecoveryDay{{Dia: "2026-08-01", Canceladas: 3, Rellenadas: 0}}}, nil
		},
	}
	called := false
	wl := &mockWaitingListReader{
		countWLBookingsFn: func(_ context.Context, _ []string) (int, error) {
			called = true
			return 0, nil
		},
	}
	h := &InternalHandler{siesaAnalytics: analytics, waitingListRepo: wl}

	req := httptest.NewRequest("GET", "/api/internal/siesa/slot-recovery", nil)
	rec := httptest.NewRecorder()
	h.HandleSiesaSlotRecovery(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if called {
		t.Error("sin ids de re-ocupación no debe consultarse el cruce WL")
	}
}
