package services

import (
	"bufio"
	"os"
	"strings"
	"testing"
)

// RONDA 2 DE LA REVISIÓN DE LA ASIGNACIÓN POR NOMBRE.

// contrastCatalog: los estudios con y sin contraste del catálogo real.
var contrastCatalog = []CupsCandidate{
	{"883230", "RESONANCIA NUCLEAR MAGNETICA DE COLUMNA LUMBOSACRA SIMPLE"},
	{"883231", "RESONANCIA MAGNETICA DE COLUMNA LUMBAR CON CONTRASTE"},
	{"883210", "RESONANCIA NUCLEAR MAGNETICA DE COLUMNA CERVICAL SIMPLE"},
	{"883211", "RESONANCIA MAGNETICA DE COLUMNA CERVICAL CON CONTRASTE"},
	{"879111", "TOMOGRAFIA AXIAL COMPUTADA DE CRANEO SIMPLE"},
	{"879112", "TOMOGRAFIA AXIAL COMPUTADA DE CRANEO CON CONTRASTE"},
	{"879113", "TOMOGRAFIA AXIAL COMPUTADA DE CRANEO SIMPLE Y CON CONTRASTE"},
	{"883101", "RESONANCIA NUCLEAR MAGNETICA DE CEREBRO"},
}

var simpleCodes = map[string]bool{"883230": true, "883210": true, "879111": true, "883101": true}

// R1-1: «con y sin contraste» (y sus variantes) es un estudio contrastado: nunca se asigna el simple, ni
// por nombre igual ni por el modelo.
func TestResolveCupsByName_WithAndWithoutContrastIsContrasted(t *testing.T) {
	for _, read := range []string{
		"RM COLUMNA LUMBAR CON Y SIN CONTRASTE", "TAC DE CRANEO CON Y SIN CONTRASTE", "TAC CRANEO SIN Y CON CONTRASTE",
		"RMN CEREBRAL CON Y SIN GADOLINIO", "RM CEREBRO SIMPLE Y CONTRASTADA",
		"RM COLUMNA CERVICAL SIN Y CON MEDIO DE CONTRASTE", "RM CERVICAL CON/SIN CONTRASTE",
		"TAC CRANEO SIN CONTRASTE Y CON CONTRASTE", "TAC CRANEO CON Y SIN MEDIO DE CONTRASTE",
		"RM COLUMNA CERVICAL C/S CONTRASTE",
	} {
		for _, code := range acceptedByAdversary(read, contrastCatalog, false) {
			if simpleCodes[code] {
				t.Errorf("%q: se asignaría el estudio SIMPLE %s", read, code)
			}
		}
	}
	if res := ResolveCupsByName("TAC DE CRANEO CON Y SIN CONTRASTE", contrastCatalog, false, nil); res.Code != "879113" {
		t.Errorf("TAC de cráneo con y sin contraste es el simple y con contraste: %+v", res)
	}
	// «CON MEDIO DE CONTRASTE» es «CON CONTRASTE»: MEDIO no es una palabra del procedimiento.
	if res := ResolveCupsByName("TAC DE CRANEO CON MEDIO DE CONTRASTE", contrastCatalog, false, nil); res.Code != "879112" {
		t.Errorf("TAC de cráneo con medio de contraste: %+v", res)
	}
}

// R1-1 en la guarda: una orden «con y sin contraste» con el código contrastado correcto no escala.
func TestCupsNamesConflict_WithAndWithoutContrastIsNotAContradiction(t *testing.T) {
	cases := []struct{ read, catalog string }{
		{"RM COLUMNA LUMBAR CON Y SIN CONTRASTE", "RESONANCIA MAGNETICA DE COLUMNA LUMBAR CON CONTRASTE"},
		{"TAC CRANEO CON Y SIN CONTRASTE", "TOMOGRAFIA AXIAL COMPUTADA DE CRANEO SIMPLE Y CON CONTRASTE"},
		{"RMN COLUMNA CERVICAL CON Y SIN CONTRASTE", "RESONANCIA MAGNETICA DE COLUMNA CERVICAL CON CONTRASTE"},
		{"TAC CRANEO SIN CONTRASTE Y CON CONTRASTE", "TOMOGRAFIA AXIAL COMPUTADA DE CRANEO SIMPLE Y CON CONTRASTE"},
		{"RM CERVICAL CON/SIN CONTRASTE", "RESONANCIA MAGNETICA DE COLUMNA CERVICAL CON CONTRASTE"},
		{"RMN CEREBRAL SIN Y CON MEDIO DE CONTRASTE", "TOMOGRAFIA AXIAL COMPUTADA DE CRANEO SIMPLE Y CON CONTRASTE"},
	}
	for _, c := range cases {
		if CupsNamesConflict(c.read, c.catalog) {
			t.Errorf("CupsNamesConflict(%q, %q) = true", c.read, c.catalog)
		}
	}
	if !CupsNamesConflict("RM COLUMNA LUMBAR CON Y SIN CONTRASTE", "RESONANCIA NUCLEAR MAGNETICA DE COLUMNA LUMBOSACRA SIMPLE") {
		t.Error("con y sin contraste frente al simple sí es contradicción")
	}
}

// Región en la guarda (variante A del revisor): dos regiones conocidas, distintas y no sinónimas son
// contradicción; los sinónimos no. Pares de testdata/cups_guard_pairs.txt (scratchpad/ocrsim/mine5).
func TestCupsNamesConflict_RegionPairsOfTheReview(t *testing.T) {
	f, err := os.Open("testdata/cups_guard_pairs.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	ok, crossed, falseAlarms, missed := 0, 0, 0, 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		p := strings.Split(line, "|")
		got := CupsNamesConflict(p[1], p[2])
		switch p[3] {
		case "OK":
			ok++
			if got {
				falseAlarms++
				t.Errorf("escala por error: %s «%s» frente a «%s»", p[0], p[1], p[2])
			}
		case "MAL":
			crossed++
			if !got {
				missed++
				t.Errorf("región cruzada sin detectar: %s «%s» frente a «%s»", p[0], p[1], p[2])
			}
		}
	}
	t.Logf("correctos que escalan: %d de %d; cruzados detectados: %d de %d", falseAlarms, ok, crossed-missed, crossed)
	// Los dos que se habían quitado en la ronda 1.
	if !CupsNamesConflict("RM DE RODILLA", "RESONANCIA MAGNETICA DE HOMBRO") {
		t.Error("rodilla frente a hombro es contradicción")
	}
	if !CupsNamesConflict("RM COLUMNA LUMBAR", "RESONANCIA MAGNETICA DE COLUMNA CERVICAL") {
		t.Error("lumbar frente a cervical es contradicción")
	}
}

// R2-1: «C/C» es «con contraste» y «S/C», «sin contraste». Antes la palabra de una letra se descartaba,
// el contraste se leía como silencio (SIMPLE) y el modelo podía confirmar el estudio simple.
func TestResolveCupsByName_SlashContrastAbbreviations(t *testing.T) {
	for _, read := range []string{
		"TAC CRANEO C/C", "TAC DE CRANEO C/C", "RM COLUMNA LUMBAR C/C", "TAC CRANEO C/ CONTRASTE",
		"TAC CRANEO C/CONTRASTE", "TAC CRANEO C/CTE", "TAC CRANEO C/MEDIO DE CONTRASTE",
		// Abreviatura con barra desconocida: el contraste no es silencio (salvo nombre igual).
		"TAC CRANEO C/X", "RM COLUMNA LUMBAR X/C",
	} {
		for _, code := range acceptedByAdversary(read, contrastCatalog, false) {
			if simpleCodes[code] {
				t.Errorf("%q: se asignaría el estudio SIMPLE %s", read, code)
			}
		}
	}
	if res := ResolveCupsByName("TAC CRANEO C/C", contrastCatalog, false, nil); res.Code != "879112" {
		t.Errorf("TAC cráneo C/C es el con contraste: %+v", res)
	}
	for _, read := range []string{"TAC CRANEO S/C", "TAC CRANEO S/CONTRASTE"} {
		if res := ResolveCupsByName(read, contrastCatalog, false, nil); res.Code != "879111" {
			t.Errorf("%q es el simple: %+v", read, res)
		}
	}
	// Las palabras con barra que ya existían no cambian: la TAC no pierde su «C» (TAC/C…).
	if got := canonicalOrderName("TAC/CONTRASTE"); !strings.Contains(got, "TOMOGRAFIA") {
		t.Errorf("TAC/CONTRASTE: %q", got)
	}
}

func TestCupsNamesConflict_SlashContrastAbbreviations(t *testing.T) {
	for _, c := range []struct{ read, catalog string }{
		{"TAC CRANEO C/C", "TOMOGRAFIA AXIAL COMPUTADA DE CRANEO CON CONTRASTE"},
		{"TAC CRANEO S/C", "TOMOGRAFIA AXIAL COMPUTADA DE CRANEO SIMPLE"},
		{"TAC CRANEO DOBLE FASE", "TOMOGRAFIA AXIAL COMPUTADA DE CRANEO CON CONTRASTE"},
		{"TAC CRANEO SIMPLE + CONTRASTE", "TOMOGRAFIA AXIAL COMPUTADA DE CRANEO SIMPLE Y CON CONTRASTE"},
		{"TAC CRANEO CONTRASTE EV", "TOMOGRAFIA AXIAL COMPUTADA DE CRANEO CON CONTRASTE"},
		{"RM COLUMNA LUMBAR CON GADOLINIO", "RESONANCIA MAGNETICA DE COLUMNA LUMBAR CON CONTRASTE"},
		{"RM COLUMNA LUMBAR SIMPLE + CONTRASTE", "RESONANCIA MAGNETICA DE COLUMNA LUMBAR CON CONTRASTE"},
		{"TAC CRANEO SIMPLE Y CONTRASTADO", "TOMOGRAFIA AXIAL COMPUTADA DE CRANEO CON CONTRASTE"},
		{"TAC CRANEO CONTRASTADO", "TOMOGRAFIA AXIAL COMPUTADA DE CRANEO SIMPLE Y CON CONTRASTE"},
		{"TAC CRANEO SIN CONTRASTE Y CON CONTRASTE", "TOMOGRAFIA AXIAL COMPUTADA DE CRANEO SIMPLE Y CON CONTRASTE"},
	} {
		if CupsNamesConflict(c.read, c.catalog) {
			t.Errorf("CupsNamesConflict(%q, %q) = true", c.read, c.catalog)
		}
	}
	if !CupsNamesConflict("TAC CRANEO C/C", "TOMOGRAFIA AXIAL COMPUTADA DE CRANEO SIMPLE") {
		t.Error("C/C frente al simple es contradicción")
	}
}
