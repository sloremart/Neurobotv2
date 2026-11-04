package handlers

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/observability"
	"github.com/neuro-bot/neuro-bot/internal/services"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
)

// ASIGNACIÓN POR NOMBRE EN VALIDATE_OCR: la orden no trae código y el bot lo resuelve (cups_name.go).

// nameAssignCatalog: procedimientos reales del catálogo, con su asunto de SIESA.
var nameAssignCatalog = []domain.Procedure{
	{Code: "890264", Name: "CONSULTA DE PRIMERA VEZ POR ESPECIALISTA EN MEDICINA FÍSICA Y REHABILITACIÓN", IsActive: true},
	{Code: "890274", Name: "CONSULTA DE PRIMERA VEZ POR ESPECIALISTA EN NEUROLOGÍA", IsActive: true},
	{Code: "890374", Name: "CONSULTA DE CONTROL O DE SEGUIMIENTO POR ESPECIALISTA EN NEUROLOGÍA", IsActive: true},
	{Code: "883101", Name: "RESONANCIA NUCLEAR MAGNETICA DE CEREBRO", IsActive: true},
	{Code: "883104", Name: "RESONANCIA MAGNETICA DE CEREBRO FUNCIONAL", IsActive: true},
	{Code: "883511", Name: "RESONANCIA MAGNETICA DE RODILLA", IsActive: true},
	{Code: "883232", Name: "RESONANCIA MAGNÉTICA DE ARTICULACIÓN SACROILÍACA SIMPLE", IsActive: true},
	{Code: "883233", Name: "RESONANCIA MAGNÉTICA DE ARTICULACIÓN SACROILÍACA CON CONTRASTE", IsActive: true},
}

var nameAssignSubjects = map[string]int{"890264": 1, "890274": 8, "890374": 9}

// runNameAssign corre VALIDATE_OCR con esos CUPS y el lector guionizado.
func runNameAssign(t *testing.T, cups []services.CUPSEntry, reader *fakeCupsReader) (*sm.StateResult, *observability.CaptureSink) {
	t.Helper()
	raw, _ := json.Marshal(cups)
	sess := testSess(sm.StateValidateOCR)
	sess.SetContext("ocr_cups_json", string(raw))
	allCalls := 0
	repo := catalogRepo(nameAssignCatalog, &allCalls)
	repo.subjectTypeFn = func(_ context.Context, code string) (int, error) { return nameAssignSubjects[code], nil }
	sink := &observability.CaptureSink{}
	stop := observability.StartCapture(sink)
	var r CupsCodeReader
	if reader != nil {
		r = reader
	}
	res, err := validateOCRHandler(repo, r)(context.Background(), sess, textM(""))
	stop()
	if err != nil {
		t.Fatal(err)
	}
	return res, sink
}

func noCode(name string) services.CUPSEntry { return services.CUPSEntry{Name: name, Quantity: 1} }

// assertNameDoubt: el procedimiento sin código no se asigna; la orden pasa al agente con su nombre.
func assertNameDoubt(t *testing.T, res *sm.StateResult, name string) {
	t.Helper()
	if res.NextState != sm.StateEscalateToAgent || res.UpdateCtx[sm.CtxEscalationReason] != sm.EscalationReasonOCRNoCode {
		t.Fatalf("%q: duda al agente: %s / %q", name, res.NextState, res.UpdateCtx[sm.CtxEscalationReason])
	}
	if !strings.Contains(res.UpdateCtx[sm.CtxOCRUnreadNames], name) || !strings.Contains(res.UpdateCtx["escalation_note"], name) {
		t.Errorf("%q: el agente ve el nombre leído: %v", name, res.UpdateCtx)
	}
	if got := firstText(t, res); got != ocrUnreadEscalationText {
		t.Errorf("aviso de siempre al paciente: %q", got)
	}
}

func TestValidateOCR_NoCodeEqualNameIsAssigned(t *testing.T) {
	reader := &fakeCupsReader{choice: "883104"}
	res, sink := runNameAssign(t, []services.CUPSEntry{noCode("RM DE RODILLA")}, reader)
	if res.NextState != sm.StateConfirmOCRResult {
		t.Fatalf("estado = %s", res.NextState)
	}
	if got := validatedCodes(t, res); !slices.Equal(got, []string{"883511"}) {
		t.Errorf("CUPS = %v", got)
	}
	if reader.choiceCalls != 0 {
		t.Error("un nombre igual no necesita el modelo")
	}
	if ev := sink.FindStep("agendar", "cups_asignado_nombre"); ev == nil || ev.Reason != services.CupsNameViaEqual {
		t.Errorf("flow_event de la asignación: %+v", ev)
	}
	var list []cupsCorrection
	_ = json.Unmarshal([]byte(res.UpdateCtx[ctxCupsNameAssignments]), &list)
	if len(list) != 1 || list[0].Used != "883511" || list[0].Name != "RM DE RODILLA" {
		t.Errorf("asignación anotada para el agente: %+v", list)
	}
}

func TestValidateOCR_NoCodeModelHighConfidenceAssigns(t *testing.T) {
	reader := &fakeCupsReader{choice: "883101"}
	res, _ := runNameAssign(t, []services.CUPSEntry{noCode("RNM CEREBRAL SIMPLE")}, reader)
	if got := validatedCodes(t, res); !slices.Equal(got, []string{"883101"}) {
		t.Fatalf("CUPS = %v (estado %s)", got, res.NextState)
	}
	if reader.choiceCalls != 1 || len(reader.lastCandidates) == 0 {
		t.Errorf("el modelo ve la lista corta: %+v", reader)
	}
}

func TestValidateOCR_NoCodeModelChoiceThatContradictsIsADoubt(t *testing.T) {
	reader := &fakeCupsReader{choice: "883104"} // FUNCIONAL, que la orden no dice
	res, _ := runNameAssign(t, []services.CUPSEntry{noCode("RNM CEREBRAL SIMPLE")}, reader)
	assertNameDoubt(t, res, "RNM CEREBRAL SIMPLE")
}

func TestValidateOCR_NoCodeMediumConfidenceIsADoubt(t *testing.T) {
	reader := &fakeCupsReader{choice: ""} // el lector devuelve "" sin confianza alta
	res, _ := runNameAssign(t, []services.CUPSEntry{noCode("RNM CEREBRAL SIMPLE")}, reader)
	assertNameDoubt(t, res, "RNM CEREBRAL SIMPLE")
	if !strings.Contains(res.UpdateCtx["escalation_note"], "883101") {
		t.Errorf("la nota trae los candidatos: %q", res.UpdateCtx["escalation_note"])
	}
}

// Sin ningún candidato no es duda (como el paso 4 de H5): se descarta como hoy, no escala la orden y
// queda anotado para el agente por si la orden escala por otro motivo.
func TestValidateOCR_NoCodeWithoutCandidatesIsDiscardedAndNoted(t *testing.T) {
	res, _ := runNameAssign(t, []services.CUPSEntry{
		{Code: "883511", Name: "RM RODILLA", Quantity: 1},
		noCode("COLONOSCOPIA TOTAL"),
	}, &fakeCupsReader{choice: "883101"})
	if res.NextState != sm.StateConfirmOCRResult {
		t.Fatalf("no escala la orden: %s / %q", res.NextState, res.UpdateCtx[sm.CtxEscalationReason])
	}
	if got := validatedCodes(t, res); !slices.Equal(got, []string{"883511"}) {
		t.Errorf("CUPS = %v", got)
	}
	if res.UpdateCtx["ocr_skipped_count"] != "1" {
		t.Errorf("se omite como hoy: %q", res.UpdateCtx["ocr_skipped_count"])
	}
	if res.UpdateCtx[sm.CtxOCRUnreadNames] != "COLONOSCOPIA TOTAL" {
		t.Errorf("queda anotado para el agente: %q", res.UpdateCtx[sm.CtxOCRUnreadNames])
	}
}

// Si la orden se queda sin ningún CUPS, se aplica la escalación de siempre de la orden sin código.
func TestValidateOCR_OnlyNoCodeWithoutCandidatesEscalatesAsBefore(t *testing.T) {
	res, _ := runNameAssign(t, []services.CUPSEntry{noCode("COLONOSCOPIA TOTAL")}, nil)
	assertNameDoubt(t, res, "COLONOSCOPIA TOTAL")
}

func TestValidateOCR_InterconsultaOnlySeesFirstVisitConsults(t *testing.T) {
	reader := &fakeCupsReader{choice: "890274"}
	cup := noCode("CONSULTA NEUROLOGIA ADULTOS")
	cup.IsInterconsulta = true
	res, _ := runNameAssign(t, []services.CUPSEntry{cup}, reader)
	for _, c := range reader.lastCandidates {
		if c.Code != "890264" && c.Code != "890274" {
			t.Errorf("una interconsulta solo ve consultas de primera vez: %+v", reader.lastCandidates)
		}
	}
	if got := validatedCodes(t, res); !slices.Equal(got, []string{"890274"}) {
		t.Errorf("CUPS = %v (estado %s)", got, res.NextState)
	}
}

// Transparencia: lo asignado por nombre muestra el nombre del catálogo y el de la orden.
func TestValidateOCR_ConfirmationShowsTheOrderName(t *testing.T) {
	res, _ := runNameAssign(t, []services.CUPSEntry{
		{Code: "883101", Name: "RNM CEREBRO", Quantity: 1},
		noCode("RM DE RODILLA"),
	}, nil)
	text := ""
	for _, m := range res.Messages {
		if b, ok := m.(*sm.ButtonMessage); ok {
			text = b.Text
		}
	}
	if !strings.Contains(text, "*RESONANCIA MAGNETICA DE RODILLA* (en tu orden: «RM DE RODILLA»)") {
		t.Errorf("el asignado muestra el nombre de la orden: %q", text)
	}
	if strings.Contains(text, "en tu orden: «RNM CEREBRO»") {
		t.Errorf("un CUPS con código se muestra como hoy: %q", text)
	}
}

// Guarda 4: un CUPS con código cuyo nombre leído CONTRADICE el del catálogo es duda; una abreviatura no.
func TestValidateOCR_CodedCupsWhoseNameContradictsIsADoubt(t *testing.T) {
	res, sink := runNameAssign(t, []services.CUPSEntry{
		{Code: "883232", Name: "RM SACROILIACAS CON CONTRASTE", Quantity: 1},
	}, nil)
	if res.NextState != sm.StateEscalateToAgent || res.UpdateCtx[sm.CtxEscalationReason] != sm.EscalationReasonCupsPossibleMisread {
		t.Fatalf("contradicción → duda: %s / %q", res.NextState, res.UpdateCtx[sm.CtxEscalationReason])
	}
	if !strings.Contains(res.UpdateCtx["escalation_note"], "883232") || sink.FindStep("agendar", "cups_duda") == nil {
		t.Errorf("la nota y el evento de la duda: %v", res.UpdateCtx)
	}

	res, _ = runNameAssign(t, []services.CUPSEntry{
		{Code: "883101", Name: "RNM CEREBRAL", Quantity: 1},
		{Code: "883233", Name: "RM SACROILIACA C/C", Quantity: 1},
	}, nil)
	if res.NextState != sm.StateConfirmOCRResult {
		t.Errorf("una abreviatura no escala: %s / %q", res.NextState, res.UpdateCtx[sm.CtxEscalationReason])
	}
}

// Un descartado sin código queda anotado aunque la orden escale por otro motivo (aquí, el tope B-10).
func TestValidateOCR_DiscardedNoCodeIsNotedWhenTheOrderEscalatesForAnotherReason(t *testing.T) {
	res, _ := runNameAssign(t, []services.CUPSEntry{
		{Code: "883511", Name: "RM RODILLA", Quantity: maxCupsQuantity + 1},
		noCode("COLONOSCOPIA TOTAL"),
	}, nil)
	if res.UpdateCtx[sm.CtxEscalationReason] != sm.EscalationReasonQuantityOutOfRange {
		t.Fatalf("escala por el tope: %q", res.UpdateCtx[sm.CtxEscalationReason])
	}
	if res.UpdateCtx[sm.CtxOCRUnreadNames] != "COLONOSCOPIA TOTAL" {
		t.Errorf("el agente ve el descartado: %q", res.UpdateCtx[sm.CtxOCRUnreadNames])
	}
}

// El nombre de la orden va en una línea y acotado.
func TestOCRSummary_OrderNameIsOneLine(t *testing.T) {
	cups := []services.CUPSEntry{{Code: "1", Name: "RM", OrderName: "RM DE\nRODILLA *DERECHA* " + strings.Repeat("X", 80), Quantity: 1}}
	msgs := ocrSummaryMessages(cups, 0)
	bm, ok := msgs[0].(*sm.ButtonMessage)
	if len(msgs) != 1 || !ok {
		t.Fatalf("una lista corta va en un solo mensaje con botones: %+v", msgs)
	}
	if strings.Contains(bm.Text, "RODILLA *DERECHA*\n") || strings.Count(bm.Text, "\n") != 4 {
		t.Errorf("el nombre de la orden va en una línea: %q", bm.Text)
	}
	if !strings.Contains(bm.Text, "…»)") {
		t.Errorf("el nombre de la orden se acota con «…»: %q", bm.Text)
	}
}
