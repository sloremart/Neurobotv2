package handlers

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/services"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
)

// activeCatalogRepo responde que todo código existe y está activo.
func activeCatalogRepo() *mockProcedureRepo {
	return &mockProcedureRepo{findByCodeFn: func(_ context.Context, code string) (*domain.Procedure, error) {
		return &domain.Procedure{ID: 1, Code: code, Name: "Proc " + code, IsActive: true}, nil
	}}
}

func validateCups(t *testing.T, cups []services.CUPSEntry) *sm.StateResult {
	t.Helper()
	raw, _ := json.Marshal(cups)
	sess := testSess(sm.StateValidateOCR)
	sess.SetContext("ocr_cups_json", string(raw))
	res, err := validateOCRHandler(activeCatalogRepo(), nil)(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// B-10: una cantidad por encima del tope no se agenda (ni se recorta): la orden pasa a un agente,
// con la nota de qué CUPS y qué cantidad.
func TestValidateOCR_QuantityAboveMaxIsEscalated(t *testing.T) {
	res := validateCups(t, []services.CUPSEntry{
		{Code: "883141", Name: "RM", Quantity: 1},
		{Code: "883210", Name: "RM columna", Quantity: services.MaxCupsQuantity + 1},
	})
	if res.NextState != sm.StateEscalateToAgent {
		t.Fatalf("esperaba ESCALATE_TO_AGENT, got %s", res.NextState)
	}
	if got := res.UpdateCtx[sm.CtxEscalationReason]; got != sm.EscalationReasonQuantityOutOfRange {
		t.Errorf("motivo = %q, quería %q", got, sm.EscalationReasonQuantityOutOfRange)
	}
	note := res.UpdateCtx["escalation_note"]
	if !strings.Contains(note, "883210") || !strings.Contains(note, "101") || strings.Contains(note, "883141") {
		t.Errorf("la nota debe nombrar solo el CUPS fuera de rango y su cantidad: %q", note)
	}
	if len(res.Messages) != 1 || res.Messages[0].(*sm.TextMessage).Text != orderUnprocessableEscalationText {
		t.Errorf("texto al paciente: %+v", res.Messages)
	}
	if _, ok := res.UpdateCtx["ocr_cups_json"]; ok {
		t.Error("no se debe reescribir la orden (ni recortar la cantidad)")
	}
}

// El tope es inclusivo: 100 sigue al resumen «¿Es correcto?» como hoy.
func TestValidateOCR_QuantityAtMaxIsAccepted(t *testing.T) {
	res := validateCups(t, []services.CUPSEntry{{Code: "883141", Name: "RM", Quantity: services.MaxCupsQuantity}})
	if res.NextState != sm.StateConfirmOCRResult {
		t.Fatalf("esperaba CONFIRM_OCR_RESULT, got %s", res.NextState)
	}
	var cups []services.CUPSEntry
	_ = json.Unmarshal([]byte(res.UpdateCtx["ocr_cups_json"]), &cups)
	if len(cups) != 1 || cups[0].Quantity != services.MaxCupsQuantity {
		t.Errorf("la cantidad no se toca: %+v", cups)
	}
}

// El campo de CUPS del agente rechaza en el formulario una cantidad por encima del tope: si no,
// VALIDATE_OCR la volvería a escalar como si fuera un lazo del bot.
func TestAgentCupsReturnInput_RejectsQuantityAboveMax(t *testing.T) {
	in := agentCupsReturnInput()
	if rej := in.Validate(nil, "883141:"+strconv.Itoa(services.MaxCupsQuantity+1)); rej.Reason == "" {
		t.Error("una cantidad mayor que el tope debe rechazarse")
	}
	if rej := in.Validate(nil, "883141:"+strconv.Itoa(services.MaxCupsQuantity)); rej.Reason != "" {
		t.Errorf("la cantidad del tope se acepta, got %q", rej.Reason)
	}
}
