package services

import (
	"slices"
	"testing"
)

// R4-M5: en un nombre de columna, D e I no son el lado («CERVICAL Y D» es la dorsal). Y el lado con puntos
// («M.D.», «O.I.») vale como la palabra.
func TestResolveCupsByName_SideLettersAndDots(t *testing.T) {
	catalog := []CupsCandidate{
		{"871010", "RADIOGRAFIA DE COLUMNA CERVICAL"},
		{"871020", "RADIOGRAFIA DE COLUMNA TORACICA"},
		{"883210", "RESONANCIA NUCLEAR MAGNETICA DE COLUMNA CERVICAL SIMPLE"},
		{"873204", "RADIOGRAFIA DE HOMBRO"},
		{"883103", "RESONANCIA NUCLEAR MAGNETICA DE ORBITAS"},
	}
	for _, read := range []string{"RX COLUMNA CERVICAL Y D", "RM COLUMNA CERVICAL Y D", "RX COLUMNA D", "RX COLUMNA I", "RX COLUMNA C Y D"} {
		if got := acceptedByAdversary(read, catalog, false); len(got) != 0 {
			t.Errorf("%q: se asignaría %v", read, got)
		}
		if res := ResolveCupsByName(read, catalog, false, nil); res.Code != "" || len(res.Candidates) == 0 {
			t.Errorf("%q: duda con candidatos: %+v", read, res)
		}
	}
	for short, word := range map[string]string{"M.D.": "DERECHA", "M.I.": "IZQUIERDA", "O.D.": "DERECHA", "O.I.": "IZQUIERDA", "D.": "DERECHA"} {
		for _, region := range []string{"RX HOMBRO ", "RM ORBITAS "} {
			got, want := acceptedByAdversary(region+short, catalog, false), acceptedByAdversary(region+word, catalog, false)
			if !slices.Equal(got, want) || len(got) != 1 {
				t.Errorf("%q: %v; con la palabra: %v", region+short, got, want)
			}
		}
		if compareReadNames("RX HOMBRO "+short, "RX HOMBRO "+word) != CupsNamesEqual {
			t.Errorf("%q no es igual a la palabra al descartar", short)
		}
	}
}
