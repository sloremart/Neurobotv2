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
		// Ronda 5b: el campo vacío tampoco.
		"TAC CRANEO CONTRASTE:", "TAC CRANEO CON CONTRASTE :", "TAC CRANEO CONTRASTE: .",
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
		for _, read := range []string{"TAC CRANEO CONTRASTE (-)", "TAC CRANEO CONTRASTE:"} {
			if !CupsNamesConflict(read, catalog) {
				t.Errorf("guarda: %q frente a «%s» debe ser duda", read, catalog)
			}
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
	// R5-I1: la abreviatura de columna con barra (L/S, D/L, C/D) no es el contraste: con su código pasa la guarda
	// sin nota, dos hojas iguales son iguales, y el contraste que sí trae el nombre se lee igual.
	for _, read := range []string{"RX COLUMNA L/S", "RX COLUMNA L/S AP Y LATERAL", "RM COLUMNA L/S SIMPLE", "RX COLUMNA C/D"} {
		if ContrastUnclear(read) {
			t.Errorf("%q: el contraste no está en duda", read)
		}
		if got := compareReadNames(read, read); got != CupsNamesEqual {
			t.Errorf("compareReadNames(%q, %q) = %v, quiero igual", read, read, got)
		}
	}
	for read, catalog := range map[string]string{
		"RX COLUMNA L/S":        "RADIOGRAFIA DE COLUMNA LUMBOSACRA",
		"RM COLUMNA L/S SIMPLE": "RESONANCIA NUCLEAR MAGNETICA DE COLUMNA LUMBOSACRA SIMPLE",
		"RX COLUMNA C/D":        "RADIOGRAFIA DE COLUMNA UNION CERVICO DORSAL",
	} {
		if CupsNamesConflict(read, catalog) {
			t.Errorf("guarda: %q frente a «%s» no contradice", read, catalog)
		}
	}
	if !CupsNamesConflict("RM COLUMNA L/S C/C", "RESONANCIA NUCLEAR MAGNETICA DE COLUMNA LUMBOSACRA SIMPLE") {
		t.Error("guarda: «RM COLUMNA L/S C/C» frente al simple sí contradice")
	}
	// R5-I4: dos frases del mismo valor, juntas, son ese valor: se asigna, pasa la guarda y entre hojas es igual.
	// R5-M1: «CON CONTRASTE NO» al final no es una respuesta: duda.
	if res := ResolveCupsByName("TAC CRANEO SIMPLE (SIN CONTRASTE)", contrastCatalog, false, nil); res.Code != "879111" {
		t.Errorf("«TAC CRANEO SIMPLE (SIN CONTRASTE)» → %q, quiero 879111", res.Code)
	}
	if CupsNamesConflict("TAC CRANEO SIMPLE (SIN CONTRASTE)", "TOMOGRAFIA AXIAL COMPUTADA DE CRANEO SIMPLE") {
		t.Error("guarda: «TAC CRANEO SIMPLE (SIN CONTRASTE)» frente al simple no contradice")
	}
	if got := compareReadNames("TAC CRANEO SIMPLE (SIN CONTRASTE)", "TAC CRANEO SIMPLE"); got != CupsNamesEqual {
		t.Errorf("compareReadNames(SIMPLE (SIN CONTRASTE), SIMPLE) = %v, quiero igual", got)
	}
	if res := ResolveCupsByName("TAC CRANEO CON CONTRASTE NO", contrastCatalog, false, nil); res.Code != "" {
		t.Errorf("«TAC CRANEO CON CONTRASTE NO» → %q, quiero duda", res.Code)
	}
	if !CupsNamesConflict("TAC CRANEO CON CONTRASTE NO", "TOMOGRAFIA AXIAL COMPUTADA DE CRANEO SIMPLE") {
		t.Error("guarda: «TAC CRANEO CON CONTRASTE NO» frente al simple es duda")
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
