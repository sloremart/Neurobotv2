package services

import "testing"

// R4-M6: lo que las mutaciones dejaron sin defender.
func TestRound4_MutationGaps(t *testing.T) {
	// La sacra solo acepta S y L5.
	sacra := "RADIOGRAFIA DE SACRO COXIS"
	if !CupsNamesConflict("RX SACRO COXIS L4", sacra) || CupsNamesConflict("RX SACRO COXIS L5", sacra) {
		t.Error("en la sacra cabe L5 y no L4")
	}
	// EXTREMIDADES solo es dónde se hace la EMG, la NC o los potenciales; en lo demás es una región que falta.
	catalog := []CupsCandidate{
		{"873420", "RADIOGRAFIA DE RODILLA AP, LATERAL"},
		{"881620", "ULTRASONOGRAFÍA ARTICULAR DE RODILLA"},
	}
	for _, read := range []string{"RX EXTREMIDADES", "ECO ARTICULAR EXTREMIDADES", "RX MIEMBROS"} {
		if got := acceptedByAdversary(read, catalog, false); len(got) != 0 {
			t.Errorf("%q: se asignaría %v", read, got)
		}
	}
	// Los niveles de tres o más letras («T12», «L4L5») tampoco buscan candidatos.
	lumbar := []CupsCandidate{{"883230", "RESONANCIA NUCLEAR MAGNETICA DE COLUMNA LUMBOSACRA SIMPLE"}}
	if res := ResolveCupsByName("RM T10 T11 T12 L4L5", lumbar, false, nil); len(res.Candidates) == 0 {
		t.Error("los niveles largos dejarían sin candidatos")
	}
}
