package services

import (
	"slices"
	"testing"
)

// Decisión del dueño: el nivel de columna es silencio solo si no contradice la región escrita (C cervical, T o D
// dorsal, L lumbar, S sacra). «RM COLUMNA CERVICAL L4» va al agente, en la asignación y en la guarda.
func TestSpineLevelMustFitTheWrittenRegion(t *testing.T) {
	catalog := []CupsCandidate{
		{"871010", "RADIOGRAFIA DE COLUMNA CERVICAL"},
		{"871020", "RADIOGRAFIA DE COLUMNA TORACICA"},
		{"871040", "RADIOGRAFIA DE COLUMNA LUMBOSACRA"},
		{"883210", "RESONANCIA NUCLEAR MAGNETICA DE COLUMNA CERVICAL SIMPLE"},
		{"883230", "RESONANCIA NUCLEAR MAGNETICA DE COLUMNA LUMBOSACRA SIMPLE"},
	}
	contradictions := []struct{ read, code, catalogName string }{
		{"RX COLUMNA CERVICAL L4", "871010", "RADIOGRAFIA DE COLUMNA CERVICAL"},
		{"RM COLUMNA CERVICAL L4", "883210", "RESONANCIA NUCLEAR MAGNETICA DE COLUMNA CERVICAL SIMPLE"},
		{"RM COLUMNA CERVICAL L4-L5", "883210", "RESONANCIA NUCLEAR MAGNETICA DE COLUMNA CERVICAL SIMPLE"},
		{"RM COLUMNA LUMBAR C5", "883230", "RESONANCIA NUCLEAR MAGNETICA DE COLUMNA LUMBOSACRA SIMPLE"},
		{"RX COLUMNA TORACICA C5", "871020", "RADIOGRAFIA DE COLUMNA TORACICA"},
		{"RX COLUMNA DORSAL L4", "871020", "RADIOGRAFIA DE COLUMNA TORACICA"},
		{"RX COLUMNA LUMBAR T5", "871040", "RADIOGRAFIA DE COLUMNA LUMBOSACRA"},
		// Sin región en el leído, la que contradice es la del catálogo.
		{"RM COLUMNA L4", "883210", "RESONANCIA NUCLEAR MAGNETICA DE COLUMNA CERVICAL SIMPLE"},
	}
	for _, c := range contradictions {
		if !CupsNamesConflict(c.read, c.catalogName) {
			t.Errorf("guarda: %q contradice «%s»", c.read, c.catalogName)
		}
		if got := acceptedByAdversary(c.read, catalog, false); slices.Contains(got, c.code) {
			t.Errorf("asignación: %q se asignaría a %s", c.read, c.code)
		}
		if res := ResolveCupsByName(c.read, catalog, false, nil); res.Code == c.code {
			t.Errorf("nombre igual: %q se asignaría a %s", c.read, c.code)
		}
	}
	// Una sola vía de comparación: con el mismo nombre del catálogo, el nivel que contradice no es silencio.
	if got := CompareCupsNames("RADIOGRAFIA DE COLUMNA CERVICAL L4", "RADIOGRAFIA DE COLUMNA CERVICAL"); got != CupsNamesDiffer {
		t.Errorf("CompareCupsNames = %v", got)
	}

	coherent := []struct{ read, catalogName string }{
		{"RM COLUMNA LUMBAR L4 L5", "RESONANCIA NUCLEAR MAGNETICA DE COLUMNA LUMBOSACRA SIMPLE"},
		{"RM COLUMNA CERVICAL C5 C6", "RESONANCIA NUCLEAR MAGNETICA DE COLUMNA CERVICAL SIMPLE"},
		{"RX COLUMNA DORSAL D7", "RADIOGRAFIA DE COLUMNA TORACICA"},
		{"RX COLUMNA TORACICA T7", "RADIOGRAFIA DE COLUMNA TORACICA"},
		// Los discos entre dos regiones son normales en una orden, y la lumbosacra incluye las sacras.
		{"RM COLUMNA LUMBOSACRA L5 S1", "RESONANCIA NUCLEAR MAGNETICA DE COLUMNA LUMBOSACRA SIMPLE"},
		{"RM COLUMNA LUMBAR S1", "RESONANCIA NUCLEAR MAGNETICA DE COLUMNA LUMBOSACRA SIMPLE"},
		{"RM COLUMNA LUMBAR T12 L1", "RESONANCIA NUCLEAR MAGNETICA DE COLUMNA LUMBOSACRA SIMPLE"},
		{"RM COLUMNA CERVICAL C7 T1", "RESONANCIA NUCLEAR MAGNETICA DE COLUMNA CERVICAL SIMPLE"},
		{"RX COLUMNA DORSAL T12 L1", "RADIOGRAFIA DE COLUMNA TORACICA"},
		// Una región sin niveles, o niveles sin región: nada que contradecir.
		{"RM COLUMNA", "RESONANCIA NUCLEAR MAGNETICA DE COLUMNA CERVICAL SIMPLE"},
		{"RM COLUMNA C5", "RESONANCIA NUCLEAR MAGNETICA DE COLUMNA CERVICAL SIMPLE"},
	}
	for _, c := range coherent {
		if CupsNamesConflict(c.read, c.catalogName) {
			t.Errorf("%q no contradice «%s»", c.read, c.catalogName)
		}
	}
	if res := ResolveCupsByName("RX COLUMNA LUMBAR L4 L5", catalog, false, nil); res.Code != "871040" {
		t.Errorf("nivel coherente: %+v", res)
	}
	if got := acceptedByAdversary("RM COLUMNA LUMBAR L5 S1", catalog, false); !slices.Contains(got, "883230") {
		t.Errorf("disco lumbosacro: %v", got)
	}
}
