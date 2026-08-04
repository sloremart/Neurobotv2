package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/config"
	"github.com/neuro-bot/neuro-bot/internal/domain"
)

// Auditoría queries M5: la conciliación consultaba cups_medico UNA VEZ POR PAR (cita, CUPS) —
// miles de round-trips a MySQL por carga de la vista SIESA. Los CUPS distintos son pocas decenas:
// se consulta una vez por CUPS y se reutiliza dentro del request.
func TestHandleSiesaConciliacion_MemoizesCupsLookups(t *testing.T) {
	rows := []domain.BotAppointmentCup{
		{CitaID: 1, CodMedi: 20, Cups: "890274", Fecha: "2026-08-01"},
		{CitaID: 2, CodMedi: 20, Cups: "890274", Fecha: "2026-08-01"},
		{CitaID: 3, CodMedi: 20, Cups: "890274", Fecha: "2026-08-02"},
		{CitaID: 4, CodMedi: 4, Cups: "879101", Fecha: "2026-08-02"},
		{CitaID: 5, CodMedi: 4, Cups: "879101", Fecha: "2026-08-03"},
		{CitaID: 6, CodMedi: 99, Cups: "879101", Fecha: "2026-08-03"},
	}
	analytics := &mockSiesaAnalyticsReader{
		botAppointmentsFn: func(_ context.Context, _ string, _ int) ([]domain.BotAppointmentCup, error) {
			return rows, nil
		},
	}
	cups := &mockCupsMedicoReader{medicos: map[string][]int{
		"890274": {20, 23},
		"879101": {4},
	}}
	h := &InternalHandler{
		siesaAnalytics: analytics,
		cupsMedico:     cups,
		cfg:            &config.Config{SIESAAssignUserCedula: "123456"},
	}
	req := httptest.NewRequest("GET", "/api/internal/siesa/conciliacion?dias=30", nil)
	rec := httptest.NewRecorder()
	h.HandleSiesaConciliacion(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if cups.calls != 2 {
		t.Errorf("FindMedicosForCups llamadas = %d, want 2 (una por CUPS distinto)", cups.calls)
	}
	var out map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	// La cita 6 (médico 99 no hace 879101) debe seguir detectándose con el memo.
	if got := out["total_mal"].(float64); got != 1 {
		t.Errorf("total_mal = %v, want 1", got)
	}
	if got := out["evaluadas_cups"].(float64); got != 6 {
		t.Errorf("evaluadas_cups = %v, want 6", got)
	}
}
