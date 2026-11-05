package services

import "testing"

// Revisión del lote C, ronda 3 (R3-M1): PROCEDIMIENTO, ESTUDIO y EXAMEN no son una consulta. Como genéricas
// igualaban «EXAMEN NEUROLOGIA» con «CONSULTA NEUROLOGIA», que son asuntos distintos.
func TestVagueWordsAreNotAConsultation(t *testing.T) {
	cases := []struct {
		a, b string
		want CupsNameMatch
	}{
		{"PROCEDIMIENTO NEUROLOGIA", "CONSULTA NEUROLOGIA", CupsNamesDiffer},
		{"PROCEDIMIENTO FISIATRIA", "CONSULTA FISIATRIA", CupsNamesDiffer},
		{"ESTUDIO NEUROLOGIA", "CONSULTA NEUROLOGIA", CupsNamesDiffer},
		{"EXAMEN NEUROLOGIA", "CONSULTA NEUROLOGIA", CupsNamesDiffer},
		{"VALORACION NEUROLOGIA", "PROCEDIMIENTO NEUROLOGIA", CupsNamesDiffer},
		{"EXAMEN NEUROLOGIA", "ESTUDIO NEUROLOGIA", CupsNamesDiffer},
		// Solas no dicen qué procedimiento es: no hay con qué comparar, aunque coincidan.
		{"ESTUDIO", "ESTUDIO", CupsNamesDiffer},
		{"EXAMEN", "PROCEDIMIENTO", CupsNamesDiffer},
		// La misma palabra con la misma especialidad sí es igual.
		{"PROCEDIMIENTO NEUROLOGIA", "PROCEDIMIENTO DE NEUROLOGIA", CupsNamesEqual},
		// La valoración sí es una consulta.
		{"VALORACION NEUROLOGIA", "CONSULTA NEUROLOGIA", CupsNamesEqual},
		// Con la modalidad escrita, el asunto ya está dicho: no cambian nada.
		{"ESTUDIO DE RM CEREBRO", "RM CEREBRO", CupsNamesEqual},
		{"EXAMEN RX TORAX", "RX DE TORAX", CupsNamesEqual},
	}
	for _, c := range cases {
		if got := compareReadNames(c.a, c.b); got != c.want {
			t.Errorf("compareReadNames(%q, %q) = %d, quiero %d", c.a, c.b, got, c.want)
		}
		if got := compareReadNames(c.b, c.a); got != c.want {
			t.Errorf("compareReadNames(%q, %q) = %d, quiero %d", c.b, c.a, got, c.want)
		}
	}
	for _, p := range [][2]string{
		{"EXAMEN NEUROLOGIA", "CONSULTA NEUROLOGIA"}, {"PROCEDIMIENTO FISIATRIA", "CONSULTA FISIATRIA"}, {"ESTUDIO", "ESTUDIO"},
	} {
		if got := CompareCupsNames(p[0], p[1]); got != CupsNamesDiffer {
			t.Errorf("CompareCupsNames(%q, %q) = %d", p[0], p[1], got)
		}
	}
}

// «PROCEDIMIENTO NEUROLOGIA» no se asigna a una consulta; lo que escribe la modalidad sigue asignándose.
func TestResolveCupsByName_VagueWords(t *testing.T) {
	catalog := []CupsCandidate{
		{"890274", "CONSULTA DE PRIMERA VEZ POR ESPECIALISTA EN NEUROLOGÍA"},
		{"890374", "CONSULTA DE CONTROL O DE SEGUIMIENTO POR ESPECIALISTA EN NEUROLOGÍA"},
		{"883101", "RESONANCIA NUCLEAR MAGNETICA DE CEREBRO"},
		{"891509", "NEUROCONDUCCION (CADA NERVIO)"},
	}
	for _, read := range []string{"PROCEDIMIENTO PRIMERA VEZ NEUROLOGIA", "EXAMEN CONTROL NEUROLOGIA", "ESTUDIO PRIMERA VEZ NEUROLOGIA"} {
		if got := acceptedByAdversary(read, catalog, false); len(got) != 0 {
			t.Errorf("%q: se asignaría la consulta %v", read, got)
		}
		if res := ResolveCupsByName(read, catalog, false, nil); len(res.Candidates) == 0 {
			t.Errorf("%q debe ir al agente con candidatos, no descartarse", read)
		}
	}
	if res := ResolveCupsByName("ESTUDIO DE RM CEREBRO", catalog, false, nil); res.Code != "883101" {
		t.Errorf("con la modalidad escrita: %+v", res)
	}
	if got := acceptedByAdversary("ESTUDIO DE NEUROCONDUCCION", catalog, false); len(got) == 0 || got[0] != "891509" {
		t.Errorf("con la modalidad escrita: %v", got)
	}
}
