package handlers

import (
	"context"
	"testing"

	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
)

// Casos REALES de invalid_input (§8.1 #4): el matching debe resolverlos sin adivinar en ambigüedad.
func TestMatchEntityByName(t *testing.T) {
	names := []string{"SANITAS", "CAPITAL SALUD", "FOMAG", "COLSANITAS", "SALUD TOTAL"}
	cases := []struct {
		in   string
		want int
	}{
		{"fomag", 3},         // exacto
		{"FOMAG", 3},         // case-insensitive
		{"capital salud", 2}, // exacto multi-palabra
		{"capital", 2},       // prefijo único
		{"4 sanitas", 1},     // dígito + exacto (caso real "4 sanitas")
		{"sanitas", 1},       // exacto gana sobre contención (COLSANITAS también contiene)
		{"colsanitas", 4},    // exacto
		{"salud", 0},         // ambiguo (CAPITAL SALUD y SALUD TOTAL) → no adivinar
		{"hola", 0},          // sin match
		{"xy", 0},            // muy corto
		{"", 0},              // vacío
		{"jersalud", 0},      // EPS que no está en la lista → no adivinar
	}
	for _, c := range cases {
		if got := matchEntityByName(c.in, names); got != c.want {
			t.Errorf("matchEntityByName(%q) = %d, esperaba %d", c.in, got, c.want)
		}
	}
}

// El nombre escrito resuelve la MISMA selección que el número (mismo código, mismo estado destino).
func TestAskEntityNumber_ByNameSelects(t *testing.T) {
	h := askEntityNumberHandler(nil)
	sess := testSess(sm.StateAskEntityNumber)
	sess.SetContext("entity_list_count", "3")
	sess.SetContext("entity_list_codes", "EPS001,EPS002,EPS003")
	sess.SetContext("entity_list_names", "SANITAS|CAPITAL SALUD|FOMAG")

	res, err := h(context.Background(), sess, textM("capital salud"))
	if err != nil {
		t.Fatal(err)
	}
	if res.NextState != sm.StateAskDocumentType {
		t.Fatalf("NextState = %s, esperaba ASK_DOCUMENT_TYPE", res.NextState)
	}
	if res.UpdateCtx["selected_entity_code"] != "EPS002" {
		t.Errorf("code = %q, esperaba EPS002", res.UpdateCtx["selected_entity_code"])
	}
}

// Ambigüedad o texto sin match → reintento (queda en el estado), NUNCA selecciona.
func TestAskEntityNumber_AmbiguousRetries(t *testing.T) {
	h := askEntityNumberHandler(nil)
	sess := testSess(sm.StateAskEntityNumber)
	sess.SetContext("entity_list_count", "2")
	sess.SetContext("entity_list_codes", "EPS001,EPS002")
	sess.SetContext("entity_list_names", "CAPITAL SALUD|SALUD TOTAL")

	res, err := h(context.Background(), sess, textM("salud"))
	if err != nil {
		t.Fatal(err)
	}
	if res.NextState != sm.StateAskEntityNumber {
		t.Errorf("ambiguo debía reintentar en el estado; got %s", res.NextState)
	}
}

// No-regresión: la ruta NUMÉRICA sigue idéntica.
func TestAskEntityNumber_NumericUnchanged(t *testing.T) {
	h := askEntityNumberHandler(nil)
	sess := testSess(sm.StateAskEntityNumber)
	sess.SetContext("entity_list_count", "3")
	sess.SetContext("entity_list_codes", "EPS001,EPS002,EPS003")
	sess.SetContext("entity_list_names", "SANITAS|CAPITAL SALUD|FOMAG")

	res, err := h(context.Background(), sess, textM("2"))
	if err != nil {
		t.Fatal(err)
	}
	if res.NextState != sm.StateAskDocumentType || res.UpdateCtx["selected_entity_code"] != "EPS002" {
		t.Errorf("ruta numérica cambió: next=%s code=%q", res.NextState, res.UpdateCtx["selected_entity_code"])
	}
}

// §8.1 #5: un botón de un paso anterior re-muestra la lista SIN gastar reintento.
func TestAskEntityNumber_StalePayloadNoRetryBurn(t *testing.T) {
	h := askEntityNumberHandler(nil)
	for _, in := range []string{"regimen_2", "ct_4"} {
		sess := testSess(sm.StateAskEntityNumber)
		sess.SetContext("entity_list_count", "2")
		sess.SetContext("entity_list_codes", "EPS001,EPS002")
		sess.SetContext("entity_list_names", "SANITAS|FOMAG")
		sess.RetryCount = 1

		res, err := h(context.Background(), sess, textM(in))
		if err != nil {
			t.Fatal(err)
		}
		if res.NextState != sm.StateAskEntityNumber {
			t.Errorf("%s: debía quedarse re-mostrando la lista; got %s", in, res.NextState)
		}
		if sess.RetryCount != 1 {
			t.Errorf("%s: NO debía gastar reintento; RetryCount=%d", in, sess.RetryCount)
		}
	}
}

// __resume__ (agente devuelve al bot) re-muestra la lista sin error ni reintento.
func TestAskEntityNumber_ResumeReshowsList(t *testing.T) {
	h := askEntityNumberHandler(nil)
	sess := testSess(sm.StateAskEntityNumber)
	sess.SetContext("entity_list_count", "2")
	sess.SetContext("entity_list_codes", "EPS001,EPS002")
	sess.SetContext("entity_list_names", "SANITAS|FOMAG")
	sess.RetryCount = 2

	res, err := h(context.Background(), sess, textM("__resume__"))
	if err != nil {
		t.Fatal(err)
	}
	if res.NextState != sm.StateAskEntityNumber || sess.RetryCount != 2 {
		t.Errorf("resume debía re-mostrar la lista sin gastar reintento; next=%s retry=%d", res.NextState, sess.RetryCount)
	}
}
