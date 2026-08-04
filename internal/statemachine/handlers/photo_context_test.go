package handlers

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/services"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
)

// La fusión de páginas dedupea por código y solo suma códigos nuevos (§8.1 #3).
func TestMergeCupsJSON(t *testing.T) {
	existing := `[{"cups_code":"890274","cups_name":"Consulta"},{"cups_code":"883101","cups_name":"RM"}]`
	merged, added := mergeCupsJSON(existing, []services.CUPSEntry{
		{Code: "883101", Name: "RM repetida"}, // duplicado → no suma
		{Code: "930860", Name: "EMG"},         // nuevo → suma
		{Code: "", Name: "sin código"},        // sin código → no suma
	})
	if added != 1 {
		t.Fatalf("added = %d, esperaba 1", added)
	}
	for _, must := range []string{"890274", "883101", "930860"} {
		if !strings.Contains(merged, must) {
			t.Errorf("merged sin %s: %s", must, merged)
		}
	}
	// Sin existente previo: todos los con-código entran.
	m2, a2 := mergeCupsJSON("", []services.CUPSEntry{{Code: "890374"}})
	if a2 != 1 || !strings.Contains(m2, "890374") {
		t.Errorf("merge sobre vacío falló: %s (%d)", m2, a2)
	}
}

// Orden como PRIMER mensaje en horario → stash + arranque normal (GREETING). Texto → pass-through.
func TestPhotoStartInterceptor(t *testing.T) {
	old := nowFunc
	nowFunc = func() time.Time { return time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC) } // miércoles 10am
	defer func() { nowFunc = old }()

	intercept := PhotoStartInterceptor(sampleConfig())

	sess := testSess(sm.StateCheckBusinessHours)
	res, ok := intercept(context.Background(), sess, imageMsg("http://x/orden.jpg"))
	if !ok {
		t.Fatal("debía interceptar imagen como primer mensaje en horario")
	}
	if res.NextState != sm.StateGreeting {
		t.Errorf("NextState = %s, esperaba GREETING (arranque normal)", res.NextState)
	}
	if !strings.Contains(res.UpdateCtx[sm.StashedOrderURLsKey], "http://x/orden.jpg") {
		t.Errorf("esperaba stash de la URL; ctx=%v", res.UpdateCtx)
	}

	if _, ok := intercept(context.Background(), testSess(sm.StateCheckBusinessHours), textM("hola")); ok {
		t.Error("texto NO debe interceptarse")
	}
	if _, ok := intercept(context.Background(), testSess(sm.StateMainMenu), imageMsg("http://x/o.jpg")); ok {
		t.Error("fuera de CHECK_BUSINESS_HOURS no intercepta (eso es de PhotoIntent)")
	}
}

// Fuera de horario: el flujo queda idéntico (OUT_OF_HOURS) y SIN stash (no puede agendar).
func TestPhotoStartInterceptor_OutOfHours(t *testing.T) {
	old := nowFunc
	nowFunc = func() time.Time { return time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC) } // domingo
	defer func() { nowFunc = old }()

	res, ok := PhotoStartInterceptor(sampleConfig())(context.Background(), testSess(sm.StateCheckBusinessHours), imageMsg("http://x/o.jpg"))
	if !ok || res.NextState != sm.StateOutOfHours {
		t.Fatalf("esperaba OUT_OF_HOURS; ok=%v res=%v", ok, res)
	}
	if res.UpdateCtx[sm.StashedOrderURLsKey] != "" {
		t.Error("fuera de horario NO debe stashear")
	}
}

// Tope de páginas adicionales: con el contador lleno no llama OCR (svc nil no revienta) y re-muestra botones.
func TestPhotoAppend_CapReached(t *testing.T) {
	intercept := PhotoAppendInterceptor(nil)
	sess := testSess(sm.StateConfirmOCRResult)
	sess.SetContext("ocr_append_count", "3")
	res, ok := intercept(context.Background(), sess, imageMsg("http://x/p4.jpg"))
	if !ok || res.NextState != sm.StateConfirmOCRResult {
		t.Fatalf("con tope debía quedarse en CONFIRM_OCR_RESULT; ok=%v", ok)
	}
	if _, ok := intercept(context.Background(), testSess(sm.StateShowSlots), imageMsg("http://x/p.jpg")); ok {
		t.Error("fuera de CONFIRM_OCR_RESULT no intercepta")
	}
}
