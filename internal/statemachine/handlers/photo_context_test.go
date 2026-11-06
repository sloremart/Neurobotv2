package handlers

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/services"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
)

// La fusión de páginas dedupea por código (o, sin código, por nombre) y solo suma lo nuevo (§8.1 #3).
// Un procedimiento sin código entra con su nombre: VALIDATE_OCR le asigna el código por nombre o lo
// pasa a un agente (antes se perdía en silencio).
func TestMergeCupsJSON(t *testing.T) {
	existing := `[{"cups_code":"890274","cups_name":"Consulta"},{"cups_code":"883101","cups_name":"RM"}]`
	merged, added := mergeCupsJSON(existing, []services.CUPSEntry{
		{Code: "883101", Name: "RM repetida"}, // duplicado → no suma
		{Code: "930860", Name: "EMG"},         // nuevo → suma
		{Code: "", Name: "Ecografía renal"},   // sin código, con nombre → suma
		{Code: "", Name: "ECOGRAFIA RENAL"},   // el mismo nombre → no suma
		{Code: "", Name: " "},                 // sin código ni nombre → no suma
	})
	if added != 2 {
		t.Fatalf("added = %d, esperaba 2", added)
	}
	for _, must := range []string{"890274", "883101", "930860", "Ecografía renal"} {
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

// Ronda 4 (R4-M1): la página añadida no pierde la duda ni la cantidad. Lo que llega marcado como duda
// (ReadDoubtWith) entra con su marca, salvo que sea IGUAL, con los mismos datos, a uno ya leído sin código (esa
// lectura sigue, así que no se pierde nada); el mismo nombre sin código con otra cantidad, sedación o contraste
// entra como duda frente al ya leído, en vez de desaparecer.
func TestMergeCupsJSON_KeepsDoubtsAndOtherQuantities(t *testing.T) {
	merged, added := mergeCupsJSON(`[{"cups_code":"879301","cups_name":"TAC TORAX"}]`, []services.CUPSEntry{
		{Name: "RX RODILLA", Quantity: 1},
		{Name: "RX RODILLA", Quantity: 3, ReadDoubtWith: "RX RODILLA"},
	})
	var out []services.CUPSEntry
	if err := json.Unmarshal([]byte(merged), &out); err != nil {
		t.Fatal(err)
	}
	if added != 2 || len(out) != 3 || out[2].Quantity != 3 || out[2].ReadDoubtWith == "" {
		t.Fatalf("la duda con cantidad 3 se pierde: added=%d %s", added, merged)
	}

	// La misma lectura con los mismos datos que una ya marcada no se repite.
	if _, added = mergeCupsJSON(merged, []services.CUPSEntry{{Name: "RX RODILLA", Quantity: 3}}); added != 0 {
		t.Errorf("la lectura ya está: added=%d", added)
	}
	// Página 1 con «RX RODILLA» (1) y página 2 con «RX RODILLA» (3), sin marca: la segunda es duda frente a la primera.
	merged, added = mergeCupsJSON(`[{"cups_code":"","cups_name":"RX RODILLA","quantity":1}]`, []services.CUPSEntry{{Name: "RX DE RODILLA", Quantity: 3}})
	_ = json.Unmarshal([]byte(merged), &out)
	if added != 1 || len(out) != 2 || out[1].ReadDoubtWith != "RX RODILLA" || out[1].Quantity != 3 {
		t.Fatalf("otra cantidad del mismo nombre es duda: added=%d %s", added, merged)
	}
	// Las marcas de cantidad del nombre también cuentan como otra lectura.
	merged, added = mergeCupsJSON(`[]`, []services.CUPSEntry{
		{Name: "RX RODILLA #1", Quantity: 1},
		{Name: "RX RODILLA #4", Quantity: 1},
	})
	_ = json.Unmarshal([]byte(merged), &out)
	if added != 2 || len(out) != 2 || out[1].ReadDoubtWith == "" {
		t.Fatalf("#1 frente a #4 es duda: added=%d %s", added, merged)
	}
	// Lo estrictamente igual sigue sin sumar.
	if _, added = mergeCupsJSON(`[{"cups_code":"","cups_name":"RX RODILLA","quantity":1}]`, []services.CUPSEntry{{Name: "RX DE RODILLA", Quantity: 1}}); added != 0 {
		t.Errorf("el mismo nombre y los mismos datos no suman: %d", added)
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
