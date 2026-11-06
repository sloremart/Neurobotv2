package services

import (
	"slices"
	"testing"
)

// R4-M3: el prefijo solo vale para el plural. ESTERNOCLAVICULAR no es ESTERNON, SACROILIACA no es
// SACROCOXIGEA, CEREBELO no es CEREBRO, y una palabra cortada (NEURO, ELECTRO) no es la entera.
func TestResolveCupsByName_PrefixIsOnlyThePlural(t *testing.T) {
	catalog := []CupsCandidate{
		{"871112", "RADIOGRAFIA DE ESTERNON"},
		{"871129", "RADIOGRAFIA DE ARTICULACIONES ESTERNOCLAVICULARES"},
		{"883234", "RESONANCIA MAGNETICA DE COLUMNA SACROCOXIGEA SIMPLE"},
		{"883232", "RESONANCIA MAGNÉTICA DE ARTICULACIÓN SACROILÍACA SIMPLE"},
		{"883101", "RESONANCIA NUCLEAR MAGNETICA DE CEREBRO"},
		{"890274", "CONSULTA DE PRIMERA VEZ POR ESPECIALISTA EN NEUROLOGÍA"},
		{"891509", "NEUROCONDUCCION (CADA NERVIO)"},
		{"930860", "ELECTROMIOGRAFÍIA EN CADA EXTREMIDAD (UNO O MAS MUSCULOS)"},
		{"873204", "RADIOGRAFIA DE HOMBRO"},
		{"881630", "ULTRASONOGRAFÍA ARTICULAR DE PELVIS (CADERA)"},
		{"879420", "TOMOGRAFIA AXIAL COMPUTADA DE ABDOMEN Y PELVIS (ABDOMEN TOTAL)"},
	}
	for read, wrong := range map[string]string{
		"RX ESTERNOCLAVICULAR": "871112", "RX ESTERNOCLAVICULARES": "871112", "RM COLUMNA SACROILIACA": "883234",
		"RM CEREBELO": "883101", "NEURO": "891509", "CONSULTA NEURO PRIMERA VEZ": "890274", "ELECTRO": "930860",
	} {
		if got := acceptedByAdversary(read, catalog, false); slices.Contains(got, wrong) {
			t.Errorf("%q: se asignaría %s: %v", read, wrong, got)
		}
	}
	// Lo cortado sigue encontrando candidatos (va al agente con ellos, no se descarta).
	for _, read := range []string{"NEURO", "ELECTRO", "RX ESTERNOCLAVICULAR", "RM CEREBELO"} {
		if res := ResolveCupsByName(read, catalog, false, nil); len(res.Candidates) == 0 {
			t.Errorf("%q debe ir al agente con candidatos", read)
		}
	}
	// El plural y los adjetivos de región siguen valiendo.
	for read, want := range map[string]string{"RX HOMBROS": "873204", "TAC ABDOMINAL Y PELVICO": "879420"} {
		if got := acceptedByAdversary(read, catalog, false); len(got) != 1 || got[0] != want {
			t.Errorf("%q: %v, quiero %s", read, got, want)
		}
	}
	for _, p := range [][2]string{{"NEURO", "NEUROLOGIA"}, {"ESTERNON", "ESTERNOCLAVICULAR"}, {"CEREBRO", "CEREBELO"}, {"RESON", "RESONANCIA"}} {
		if pluralWord(p[0], p[1]) {
			t.Errorf("pluralWord(%q, %q) = true", p[0], p[1])
		}
	}
	for _, p := range [][2]string{{"RODILLA", "RODILLAS"}, {"EXTREMIDAD", "EXTREMIDADES"}, {"MANO", "MANOS"}, {"VISUAL", "VISUALES"}} {
		if !pluralWord(p[0], p[1]) || !pluralWord(p[1], p[0]) {
			t.Errorf("pluralWord(%q, %q) = false", p[0], p[1])
		}
	}
	// El prefijo sigue sirviendo para sospechar: la consulta cortada es duda, no otra cosa.
	if got := CompareCupsNames("CONSULTA OTORRINO", "CONSULTA DE OTORRINOLARINGOLOGIA"); got != CupsNamesByPrefix {
		t.Errorf("CompareCupsNames(OTORRINO) = %d", got)
	}
}
