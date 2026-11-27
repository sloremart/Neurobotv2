package handlers

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/repository/local"
	"github.com/neuro-bot/neuro-bot/internal/services"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
)

// redirectCatalog: una infiltración (aplicación de medicamento) y una RM simple del catálogo.
var redirectCatalog = map[string]string{
	"861401": "INFILTRACION INTRALESIONAL CON MEDICAMENTO HASTA DE CINCO LESIONES",
	"883232": "RESONANCIA MAGNETICA DE ARTICULACIONES SACROILIACAS SIMPLE",
}

// validateWithMedRepo corre VALIDATE_OCR con un medRepo no nil (la redirección solo mira que lo
// haya; no consulta la BD en este paso).
func validateWithMedRepo(t *testing.T, cups []services.CUPSEntry) *sm.StateResult {
	t.Helper()
	repo := &mockProcedureRepo{findByCodeFn: func(_ context.Context, code string) (*domain.Procedure, error) {
		if name, ok := redirectCatalog[code]; ok {
			return &domain.Procedure{ID: 1, Code: code, Name: name, IsActive: true}, nil
		}
		return nil, nil
	}}
	raw, _ := json.Marshal(cups)
	sess := testSess(sm.StateValidateOCR)
	sess.SetContext("ocr_cups_json", string(raw))
	res, err := validateOCRHandler(repo, nil, local.NewMedicationPendingRepo(nil))(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// Una orden de aplicación de medicamento sola sigue por el flujo de medicamentos, como en main.
func TestValidateOCR_MedicationApplicationRedirects(t *testing.T) {
	res := validateWithMedRepo(t, []services.CUPSEntry{
		{Code: "861401", Name: "INFILTRACION INTRALESIONAL", Quantity: 1},
	})
	if res.NextState != sm.StateMedicationCheckExcel {
		t.Fatalf("la infiltración sola va al flujo de medicamentos: %s / %q", res.NextState, res.UpdateCtx[sm.CtxEscalationReason])
	}
}

// Una duda de la orden llega al agente antes que la redirección: si no, se perdería en el flujo de
// medicamentos (o del todo, si el paciente no está en medication_pending).
func TestValidateOCR_DoubtBeatsMedicationRedirect(t *testing.T) {
	cases := []struct {
		name string
		cups []services.CUPSEntry
		want string
	}{
		{"lectura dudosa", []services.CUPSEntry{
			{Code: "861401", Name: "INFILTRACION INTRALESIONAL", Quantity: 1},
			{Code: "883232", Name: "RM SACROILIACAS CON CONTRASTE", Quantity: 1},
		}, sm.EscalationReasonCupsPossibleMisread},
		{"cantidad fuera de rango", []services.CUPSEntry{
			{Code: "861401", Name: "INFILTRACION INTRALESIONAL", Quantity: services.MaxCupsQuantity + 1},
		}, sm.EscalationReasonQuantityOutOfRange},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := validateWithMedRepo(t, tc.cups)
			if res.NextState != sm.StateEscalateToAgent || res.UpdateCtx[sm.CtxEscalationReason] != tc.want {
				t.Fatalf("la duda va al agente con su motivo: %s / %q, quería %q",
					res.NextState, res.UpdateCtx[sm.CtxEscalationReason], tc.want)
			}
		})
	}
}
