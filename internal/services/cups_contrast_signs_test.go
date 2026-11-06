package services

import "testing"

// Revisión del lote C, ronda 5 (R5-C1): el signo que niega, afirma o deja vacío el contraste («CONTRASTE (-)»,
// «Ø CONTRASTE», «CONTRASTE: 0», «CON CONTRASTE ( )») lo veía la normalización antes que el clasificador, y lo
// borraba: la lectura quedaba CON CONTRASTE y se asignaba por nombre igual. Ahora el signo es una ficha propia,
// la frase no está en la gramática y es DESCONOCIDO: al agente, al asignar, al descartar y en la guarda.
func TestContrastSign_NeverAssignsAContrastedStudy(t *testing.T) {
	reads := []string{
		"TAC CRANEO CONTRASTE (-)", "TAC CRANEO CONTRASTE(-)", "TAC CRANEO CONTRASTE: ---", "TAC CRANEO (-) CONTRASTE",
		"RM COLUMNA LUMBAR CONTRASTE (-)", "RM COLUMNA LUMBAR CON CONTRASTE (-)", "RM COLUMNA CERVICAL GADOLINIO (-)",
		"TAC CRANEO Ø CONTRASTE", "TAC CRANEO CONTRASTE: Ø", "TAC CRANEO CONTRASTE: 0", "TAC CRANEO CONTRASTE (0)",
		"TAC CRANEO CONTRASTE = 0", "TAC CRANEO CON CONTRASTE ( )", "TAC CRANEO CONTRASTE: [ ]", "TAC CRANEO CONTRASTE ✗",
		"TAC CRANEO CONTRASTE (+)", "TAC CRANEO SIMPLE (-)",
	}
	for _, read := range reads {
		if res := ResolveCupsByName(read, contrastCatalog, false, nil); res.Code != "" {
			t.Errorf("%q: se asignaría %s por nombre igual", read, res.Code)
		}
		if got := acceptedByAdversary(read, contrastCatalog, false); len(got) != 0 {
			t.Errorf("%q: el modelo podría confirmar %v", read, got)
		}
		if res := ResolveCupsByName(read, contrastCatalog, false, nil); len(res.Candidates) == 0 {
			t.Errorf("%q debe ir al agente con candidatos", read)
		}
		if !ContrastUnclear(read) {
			t.Errorf("%q: el contraste debe quedar en duda", read)
		}
	}
	// Con código, la guarda escala: con el contrastado y con el simple.
	for _, catalog := range []string{
		"TOMOGRAFIA AXIAL COMPUTADA DE CRANEO CON CONTRASTE", "TOMOGRAFIA AXIAL COMPUTADA DE CRANEO SIMPLE",
	} {
		if !CupsNamesConflict("TAC CRANEO CONTRASTE (-)", catalog) {
			t.Errorf("guarda: «TAC CRANEO CONTRASTE (-)» frente a «%s» debe ser duda", catalog)
		}
	}
	// Entre hojas, el signo frente a la palabra es duda, nunca igual.
	for _, pair := range [][2]string{
		{"TAC CRANEO CONTRASTE (-)", "TAC CRANEO CON CONTRASTE"},
		{"TAC CRANEO CON CONTRASTE ( )", "TAC CRANEO CONTRASTADO"},
		{"TAC CRANEO Ø CONTRASTE", "TAC CRANEO CONTRASTADO"},
		{"TAC CRANEO CONTRASTE: 0", "TAC CRANEO SIMPLE"},
	} {
		if got := compareReadNames(pair[0], pair[1]); got != CupsNamesByPrefix {
			t.Errorf("compareReadNames(%q, %q) = %v, quiero duda", pair[0], pair[1], got)
		}
	}
	// Un signo lejos de la mención no cambia nada: el guion entre niveles o entre regiones es un separador.
	for read, want := range map[string]string{
		"RM COLUMNA LUMBAR L4 - L5 CON CONTRASTE": "883231",
		"TAC CRANEO - SIMPLE":                     "",
		"TAC CRANEO CONTRASTE: NO":                "879111",
		"TAC CRANEO CONTRASTADO (NO)":             "879111",
	} {
		res := ResolveCupsByName(read, contrastCatalog, false, nil)
		if res.Code != want {
			t.Errorf("%q → %q, quiero %q", read, res.Code, want)
		}
	}
}
