package services

import "testing"

// R4-M2: «VEZ» sola no es la primera vez. «2 VEZ», «VEZ 2» o «VEZ» a secas no encajan con la consulta de
// primera vez; «1 VEZ» y «1A VEZ» sí.
func TestResolveCupsByName_OnlyFirstVisitIsFirstVisit(t *testing.T) {
	catalog := []CupsCandidate{
		{"890274", "CONSULTA DE PRIMERA VEZ POR ESPECIALISTA EN NEUROLOGÍA"},
		{"890374", "CONSULTA DE CONTROL O DE SEGUIMIENTO POR ESPECIALISTA EN NEUROLOGÍA"},
		{"890264", "CONSULTA DE PRIMERA VEZ POR ESPECIALISTA EN MEDICINA FÍSICA Y REHABILITACIÓN"},
	}
	for _, read := range []string{
		"CONSULTA 2 VEZ NEUROLOGIA", "CONSULTA NEUROLOGIA VEZ 2", "CONSULTA 3 VEZ FISIATRIA", "CONSULTA NEUROLOGIA VEZ",
		"CONSULTA SEGUNDA VEZ NEUROLOGIA",
	} {
		if got := acceptedByAdversary(read, catalog, false); len(got) != 0 {
			t.Errorf("%q: se asignaría %v", read, got)
		}
		if res := ResolveCupsByName(read, catalog, false, nil); len(res.Candidates) == 0 {
			t.Errorf("%q debe ir al agente con candidatos", read)
		}
	}
	for _, read := range []string{"CONSULTA 1 VEZ NEUROLOGIA", "CONSULTA 1A VEZ NEUROLOGIA", "CONSULTA 1ª VEZ NEUROLOGIA", "CONSULTA PRIMERA VEZ NEUROLOGIA"} {
		if got := acceptedByAdversary(read, catalog, false); len(got) != 1 || got[0] != "890274" {
			t.Errorf("%q: %v, quiero 890274", read, got)
		}
	}
}
