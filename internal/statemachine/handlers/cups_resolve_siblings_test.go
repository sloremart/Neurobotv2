package handlers

import (
	"slices"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/observability"
	"github.com/neuro-bot/neuro-bot/internal/services"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
)

// HERMANOS REALES DEL CATÁLOGO (migrations/019_create_cups_catalog.up.sql). La revisión de la ronda 2
// encontró que la cascada corregía a OTRO procedimiento real: el código de la orden existía pero no
// estaba activo (o faltaba), y su hermano a un dígito tenía un nombre «parecido». Ninguno de estos pares
// puede corregirse.

// siblingPairs: el código leído (con su nombre real, como lo escribe la orden) y su hermano.
var siblingPairs = []struct {
	read, sibling domain.Procedure
}{
	{
		domain.Procedure{Code: "881502", Name: "ULTRASONOGRAFÍA DE PROSTATA TRANSRECTAL"},
		domain.Procedure{Code: "881501", Name: "ULTRASONOGRAFÍA DE PROSTATA TRANSABDOMINAL  (62)"},
	},
	{
		domain.Procedure{Code: "883104", Name: "RESONANCIA MAGNETICA DE CEREBRO FUNCIONAL"},
		domain.Procedure{Code: "883101", Name: "RESONANCIA NUCLEAR MAGNETICA DE CEREBRO"},
	},
	{
		domain.Procedure{Code: "883106", Name: "RESONANCIA MAGNETICA DE CEREBRO CON TRACTOGRAFIA"},
		domain.Procedure{Code: "883101", Name: "RESONANCIA NUCLEAR MAGNETICA DE CEREBRO"},
	},
	{
		domain.Procedure{Code: "881360", Name: "ULTRASONOGRAFIA PELVICA CON ANALISIS DOPPLER ACR"},
		domain.Procedure{Code: "881362", Name: "ULTRASONOGRAFIA DE MASAS DE TEJIDOS BLANDOS DE ABDOMEN CON ANALISIS DOPPLER"},
	},
	{
		domain.Procedure{Code: "890275", Name: "CONSULTA DE PRIMERA VEZ POR ESPECIALISTA EN NEUROLOGÍA PEDIÁTRICA"},
		domain.Procedure{Code: "890375", Name: "CONSULTA DE CONTROL O DE SEGUIMIENTO POR ESPECIALISTA EN NEUROLOGÍA PEDIÁTRICA"},
	},
	{
		domain.Procedure{Code: "890375", Name: "CONSULTA DE CONTROL O DE SEGUIMIENTO POR ESPECIALISTA EN NEUROLOGÍA PEDIÁTRICA"},
		domain.Procedure{Code: "890275", Name: "CONSULTA DE PRIMERA VEZ POR ESPECIALISTA EN NEUROLOGÍA PEDIÁTRICA"},
	},
	{
		domain.Procedure{Code: "921402", Name: "TOMOGRAFIA POR EMISION DE POSITRONES(PET-TC) CON 18-FLUOR"},
		domain.Procedure{Code: "921405", Name: "TOMOGRAFIA POR EMISION DE POSITRONES(PET-TC) CON PSMA"},
	},
	{
		domain.Procedure{Code: "921405", Name: "TOMOGRAFIA POR EMISION DE POSITRONES(PET-TC) CON PSMA"},
		domain.Procedure{Code: "921402", Name: "TOMOGRAFIA POR EMISION DE POSITRONES(PET-TC) CON 18-FLUOR"},
	},
	{
		domain.Procedure{Code: "883232", Name: "RESONANCIA MAGNÉTICA DE ARTICULACIÓN SACROILÍACA SIMPLE"},
		domain.Procedure{Code: "883233", Name: "RESONANCIA MAGNÉTICA DE ARTICULACIÓN SACROILÍACA CON CONTRASTE"},
	},
	{
		domain.Procedure{Code: "870112", Name: "RADIOGRAFIA DE MAXILAR SUPERIOR"},
		domain.Procedure{Code: "870113", Name: "RADIOGRAFIA DE MAXILAR INFERIOR"},
	},
	{
		domain.Procedure{Code: "870113", Name: "RADIOGRAFIA DE MAXILAR INFERIOR"},
		domain.Procedure{Code: "870112", Name: "RADIOGRAFIA DE MAXILAR SUPERIOR"},
	},
	// Revisión ronda 3 (R2-1): bilateral frente a unilateral, solo por prefijo.
	{
		domain.Procedure{Code: "882317", Name: "ECOGRAFIA DOPPLER DE VASOS VENOSOS DE MIEMBROS INFERIORES"},
		domain.Procedure{Code: "882318", Name: "ECOGRAFÍA DOPPLER DE VASOS VENOSOS DE MIEMBRO INFERIOR"},
	},
	{
		domain.Procedure{Code: "882318", Name: "ECOGRAFÍA DOPPLER DE VASOS VENOSOS DE MIEMBRO INFERIOR"},
		domain.Procedure{Code: "882317", Name: "ECOGRAFIA DOPPLER DE VASOS VENOSOS DE MIEMBROS INFERIORES"},
	},
	{
		domain.Procedure{Code: "882309", Name: "ECOGRAFIA DOPPLER DE VASOS VENOSOS DE MIEMBROS SUPERIORES"},
		domain.Procedure{Code: "882316", Name: "ECOGRAFIA DOPPLER DE VASOS VENOSOS DE MIEMBRO SUPERIOR"},
	},
	{
		domain.Procedure{Code: "882316", Name: "ECOGRAFIA DOPPLER DE VASOS VENOSOS DE MIEMBRO SUPERIOR"},
		domain.Procedure{Code: "882309", Name: "ECOGRAFIA DOPPLER DE VASOS VENOSOS DE MIEMBROS SUPERIORES"},
	},
}

// prefixOnlyPairs son los pares cuyos nombres coinciden solo por prefijo (singular/plural, bilateral
// frente a unilateral): con el código ausente el caso es DUDA y va al agente; nunca se corrige.
var prefixOnlyPairs = map[string]bool{"882317": true, "882318": true, "882309": true, "882316": true}

func withActive(p domain.Procedure, isActive bool) domain.Procedure {
	p.IsActive = isActive
	return p
}

// otherProc es el procedimiento bien leído que acompaña a cada orden de prueba.
var otherProc = withActive(domain.Procedure{Code: "883401", Name: "RESONANCIA MAGNETICA DE ABDOMEN"}, true)

func orderWith(read domain.Procedure) []services.CUPSEntry {
	return []services.CUPSEntry{
		{Code: otherProc.Code, Name: otherProc.Name, Quantity: 1},
		{Code: read.Code, Name: read.Name, Quantity: 1},
	}
}

// assertDiscardedAsToday: el código leído no se corrige ni deja duda; se omite como siempre y el resto
// de la orden sigue.
func assertDiscardedAsToday(t *testing.T, res *sm.StateResult, sink *observability.CaptureSink, read string) {
	t.Helper()
	if res.NextState != sm.StateConfirmOCRResult {
		t.Fatalf("%s: estado = %s (motivo %q)", read, res.NextState, res.UpdateCtx[sm.CtxEscalationReason])
	}
	if got := validatedCodes(t, res); !slices.Equal(got, []string{otherProc.Code}) {
		t.Errorf("%s: se agenda otro estudio: %v", read, got)
	}
	if got := corrections(t, res); len(got) != 0 || sink.FindStep("agendar", "cups_corregido") != nil {
		t.Errorf("%s: no puede corregirse a un hermano: %+v", read, got)
	}
	if res.UpdateCtx["ocr_skipped_count"] != "1" {
		t.Errorf("%s: se omite como hoy: %q", read, res.UpdateCtx["ocr_skipped_count"])
	}
}

// 1a: un código que EXISTE en el catálogo pero está inactivo es real y Neuro no lo agenda: se descarta,
// sin cascada.
func TestValidateOCR_InactiveSiblingsAreNeverCorrected(t *testing.T) {
	for _, pair := range siblingPairs {
		catalog := []domain.Procedure{withActive(pair.read, false), withActive(pair.sibling, true), otherProc}
		res, sink, _ := runValidateWith(t, catalog, orderWith(pair.read), "", nil)
		assertDiscardedAsToday(t, res, sink, pair.read.Code)
	}
}

// 1a, aislado del parecido: aunque el hermano a un dígito tenga el MISMO nombre y la segunda lectura lo
// señale, el inactivo no entra a la cascada.
func TestValidateOCR_InactiveCodeSkipsTheCascade(t *testing.T) {
	read := domain.Procedure{Code: "881502", Name: "ULTRASONOGRAFÍA DE PROSTATA TRANSRECTAL"}
	twin := withActive(domain.Procedure{Code: "881503", Name: read.Name}, true)
	reader := &fakeCupsReader{code: "881503"}
	catalog := []domain.Procedure{withActive(read, false), twin, otherProc}
	res, sink, allCalls := runValidateWith(t, catalog, orderWith(read), testOrderURL, reader)
	assertDiscardedAsToday(t, res, sink, read.Code)
	if reader.calls != 0 || allCalls != 0 {
		t.Errorf("un inactivo no entra a la cascada: lecturas=%d catálogo=%d", reader.calls, allCalls)
	}
}

// 1c y 1d: si el código falta del catálogo (p. ej. el dashboard lo quitó), su hermano real a un dígito
// tampoco se parece: los nombres difieren en una palabra distintiva (región, lateralidad, contraste,
// tipo de consulta, pediatría, vía, marcador del PET, funcional o tractografía).
func TestValidateOCR_AbsentCodeIsNotCorrectedToARealSibling(t *testing.T) {
	for _, pair := range siblingPairs {
		catalog := []domain.Procedure{withActive(pair.sibling, true), otherProc}
		res, sink, _ := runValidateWith(t, catalog, orderWith(pair.read), "", nil)
		if prefixOnlyPairs[pair.read.Code] {
			assertDoubtNotCorrected(t, res, sink, pair.read.Code)
			continue
		}
		assertDiscardedAsToday(t, res, sink, pair.read.Code)
	}
}

// assertDoubtNotCorrected: el código leído no se corrige; la orden pasa al agente como duda.
func assertDoubtNotCorrected(t *testing.T, res *sm.StateResult, sink *observability.CaptureSink, read string) {
	t.Helper()
	if res.NextState != sm.StateEscalateToAgent || res.UpdateCtx[sm.CtxEscalationReason] != sm.EscalationReasonCupsPossibleMisread {
		t.Errorf("%s: coincidencia solo por prefijo → duda: %s / %q", read, res.NextState, res.UpdateCtx[sm.CtxEscalationReason])
	}
	if sink.FindStep("agendar", "cups_corregido") != nil {
		t.Errorf("%s: no puede corregirse", read)
	}
}

// R2-1: la segunda lectura que apunta a un código cuyo nombre coincide solo por prefijo tampoco corrige:
// es duda.
func TestValidateOCR_SecondReadingWithPrefixOnlyNameIsADoubt(t *testing.T) {
	read := domain.Procedure{Code: "882399", Name: "ECOGRAFIA DOPPLER DE VASOS VENOSOS DE MIEMBROS INFERIORES"}
	unilateral := withActive(domain.Procedure{Code: "882318", Name: "ECOGRAFÍA DOPPLER DE VASOS VENOSOS DE MIEMBRO INFERIOR"}, true)
	reader := &fakeCupsReader{code: "882318"}
	res, sink, _ := runValidateWith(t, []domain.Procedure{unilateral, otherProc}, orderWith(read), testOrderURL, reader)
	assertDoubtNotCorrected(t, res, sink, read.Code)
}

// 1b: si la segunda lectura devuelve el MISMO código leído, el código está confirmado y no se corrige
// por nombre aunque haya un único nombre igual a un dígito; como no está en el catálogo, se descarta.
func TestValidateOCR_SecondReadingConfirmsTheCode(t *testing.T) {
	read := domain.Procedure{Code: "890279", Name: "CONSULTA PRIMERA VEZ NEUROLOGIA"}
	reader := &fakeCupsReader{code: "890279"}
	catalog := append([]domain.Procedure{otherProc}, resolveCatalog...)
	res, sink, _ := runValidateWith(t, catalog, orderWith(read), testOrderURL, reader)
	assertDiscardedAsToday(t, res, sink, read.Code)
	if reader.calls != 1 {
		t.Errorf("la segunda lectura se pidió %d veces", reader.calls)
	}
}

// 2: la segunda lectura también exige que el nombre del código nuevo sea el del procedimiento leído.
// La lectura dedicada de «RM de columna» que devuelve el código de la fila vecina (cerebro) no corrige.
func TestValidateOCR_SecondReadingNeedsTheSameName(t *testing.T) {
	read := domain.Procedure{Code: "883299", Name: "RESONANCIA MAGNETICA DE COLUMNA LUMBAR SIMPLE"}
	reader := &fakeCupsReader{code: "883101"}
	catalog := []domain.Procedure{withActive(domain.Procedure{Code: "883101", Name: "RESONANCIA MAGNETICA DE CEREBRO"}, true), otherProc}
	res, sink, _ := runValidateWith(t, catalog, orderWith(read), testOrderURL, reader)
	assertDiscardedAsToday(t, res, sink, read.Code)
}

// 2: si el código corregido YA está en la orden, no se corrige: es duda (la orden quedaría duplicada y
// el procedimiento leído, perdido).
func TestValidateOCR_CorrectionToACodeAlreadyInTheOrderIsADoubt(t *testing.T) {
	read := domain.Procedure{Code: "883491", Name: otherProc.Name}
	reader := &fakeCupsReader{code: otherProc.Code}
	res, _, _ := runValidateWith(t, []domain.Procedure{otherProc}, orderWith(read), testOrderURL, reader)
	if res.NextState != sm.StateEscalateToAgent || res.UpdateCtx[sm.CtxEscalationReason] != sm.EscalationReasonCupsPossibleMisread {
		t.Fatalf("duda al agente: %s / %q", res.NextState, res.UpdateCtx[sm.CtxEscalationReason])
	}
}
