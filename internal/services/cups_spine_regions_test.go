package services

import (
	"slices"
	"testing"
)

// R4-M4: el nivel se mira en todas las regiones escritas. DORSOLUMBAR y SACROCOXIGEA también; LUMBAR acepta
// S1 (la lumbosacra) pero no S2 en adelante; el disco entre dos regiones cabe en las dos.
func TestSpineLevel_AllWrittenRegions(t *testing.T) {
	catalog := []CupsCandidate{
		{"871030", "RADIOGRAFIA DE COLUMNA DORSOLUMBAR"},
		{"883234", "RESONANCIA MAGNETICA DE COLUMNA SACROCOXIGEA SIMPLE"},
		{"883230", "RESONANCIA NUCLEAR MAGNETICA DE COLUMNA LUMBOSACRA SIMPLE"},
		{"871050", "RADIOGRAFIA DE SACRO COXIS"},
		{"879201", "TOMOGRAFIA AXIAL COMPUTADA DE COLUMNA SEGMENTOS CERVICAL, TORACICO, LUMBAR Y/O SACRO, POR CADA NIVEL (TRES ESPACIOS)"},
	}
	contradictions := []struct{ read, code string }{
		{"RX COLUMNA DORSOLUMBAR C5", "871030"},
		{"RX COLUMNA DORSOLUMBAR S3", "871030"},
		{"RM COLUMNA SACROCOXIGEA L4", "883234"},
		{"RM COLUMNA SACROCOXIGEA C5", "883234"},
		{"RM COLUMNA LUMBAR S3", "883230"},
		{"RM COLUMNA LUMBOSACRA S2", "883230"},
		{"RX SACRO COXIS L4", "871050"},
	}
	for _, c := range contradictions {
		name := catalog[slices.IndexFunc(catalog, func(x CupsCandidate) bool { return x.Code == c.code })].Name
		if !CupsNamesConflict(c.read, name) {
			t.Errorf("guarda: %q contradice «%s»", c.read, name)
		}
		if got := acceptedByAdversary(c.read, catalog, false); slices.Contains(got, c.code) {
			t.Errorf("%q se asignaría a %s", c.read, c.code)
		}
		if res := ResolveCupsByName(c.read, catalog, false, nil); res.Code == c.code {
			t.Errorf("nombre igual: %q se asignaría a %s", c.read, c.code)
		}
	}
	coherent := []struct{ read, code string }{
		{"RX COLUMNA DORSOLUMBAR T12 L1", "871030"},
		{"RX COLUMNA DORSOLUMBAR L5 S1", "871030"},
		{"RM COLUMNA SACROCOXIGEA S2", "883234"},
		{"RM COLUMNA SACROCOXIGEA L5 S1", "883234"},
		{"RM COLUMNA LUMBAR S1", "883230"},
		{"RM COLUMNA LUMBOSACRA L5 S1", "883230"},
		{"RM COLUMNA LUMBAR T12 L1", "883230"},
		{"RX SACRO COXIS L5 S1", "871050"},
		// TORACICO es la región dorsal (879201 escribe las cuatro): T6-T7 no la contradice.
		{"TAC COLUMNA T6 T7", "879201"},
		{"TAC COLUMNA DORSAL T6 T7", "879201"},
	}
	for _, c := range coherent {
		name := catalog[slices.IndexFunc(catalog, func(x CupsCandidate) bool { return x.Code == c.code })].Name
		if CupsNamesConflict(c.read, name) {
			t.Errorf("%q no contradice «%s»", c.read, name)
		}
	}
	if got := acceptedByAdversary("RM COLUMNA SACROCOXIGEA S2", catalog, false); !slices.Contains(got, "883234") {
		t.Errorf("sacrocoxígea S2: %v", got)
	}
}

// Ronda 6 (R6-M2): el coxis es segmento de columna por sí mismo (no solo región): el nivel se mira contra él.
func TestSpineLevel_CoxisIsASegment(t *testing.T) {
	const sacroCoxis = "RADIOGRAFIA DE SACRO COXIS"
	for read, want := range map[string]bool{
		"RX COXIS L4": true, "RX COCCIX L4": true, "RX COXIS C5": true,
		"RX COXIS S4": false, "RX COXIS L5 S1": false, "RX SACRO S2": false,
	} {
		if got := CupsNamesConflict(read, sacroCoxis); got != want {
			t.Errorf("CupsNamesConflict(%q, sacro coxis) = %v, quiero %v", read, got, want)
		}
	}
	if !levelsContradictRegion(canonicalOrderName("RX COXIS L4")) {
		t.Error("«RX COXIS L4»: L4 no cabe en el coxis")
	}
}
