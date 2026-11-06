package services

import (
	"slices"
	"testing"
)

// Revisión del lote C, ronda 5 (R5-I3, preexistente): una consulta o una EMG con una cola típica de la orden —el
// plazo («EN 1 MES», «6 MESES»), el diagnóstico («CIE10 M545», «G40») o el motivo («POR DOLOR»)— se quedaba sin
// candidatos y se descartaba en silencio. El plazo y el CIE son ruido de la orden al asignar; la cola «POR …» se
// quita solo si sin ella hay candidatos, y lo que queda sigue el camino normal: se asigna solo si encaja.
func TestResolveCupsByName_OrderTailsNeverDiscardSilently(t *testing.T) {
	catalog := catalogFromTSV(t, "testdata/cups_catalog_active.tsv")
	for _, read := range []string{
		"CONSULTA NEUROLOGIA EN 1 MES", "CONSULTA NEUROLOGIA EN 3 MESES", "CONSULTA NEUROLOGIA 1 MES",
		"CONSULTA NEUROLOGIA 6 MESES", "CONSULTA NEUROLOGIA PARA 1 MES", "CONSULTA NEUROLOGIA EN 2 SEMANAS",
		"CONSULTA NEUROLOGIA EN 15 DIAS", "CONSULTA NEUROLOGIA CITA PRIORITARIA", "CONSULTA NEUROLOGIA CIE10 M545",
		"CONSULTA NEUROLOGIA CIE-10: G40", "CONSULTA NEUROLOGIA M54.5", "CONSULTA NEUROLOGIA POR CEFALEA",
		"EMG MMII POR DOLOR", "EMG MMII POR DOLOR LUMBAR POR TRAUMA", "EMG MMII EN 3 MESES", "EMG MMII 6 MESES",
		"EMG MMII CIE10 M545", "EMG MMII PRIORITARIA", "EMG MMII CITA PRIORITARIA",
	} {
		if res := ResolveCupsByName(read, catalog, false, nil); len(res.Candidates) == 0 {
			t.Errorf("%q: sin candidatos, se descartaría en silencio", read)
		}
	}
	// Lo que queda tras la cola se asigna solo si encaja (como sin la cola).
	for read, want := range map[string][]string{
		"CONSULTA CONTROL NEUROLOGIA EN 3 MESES": {"890374"},
		"CONSULTA NEUROLOGIA CONTROL CIE10 G40":  {"890374"},
		"EMG MMII POR DOLOR":                     {"930860"},
		"EMG MMII EN 6 MESES":                    {"930860"},
		"RM COLUMNA LUMBAR T12 L1 CIE10 M545":    {"883230"},
		"CONSULTA NEUROLOGIA EN 1 MES":           nil,
		"CONSULTA NEUROLOGIA POR CEFALEA":        nil,
	} {
		if got := acceptedByAdversary(read, catalog, false); !slices.Equal(got, want) {
			t.Errorf("%q → %v, quiero %v", read, got, want)
		}
	}
	// Un nombre con POR que sí tiene candidatos no se corta.
	if res := ResolveCupsByName("ONDA F POR NERVIO", catalog, false, nil); res.Code != "891514" {
		t.Errorf("«ONDA F POR NERVIO» → %q, quiero 891514", res.Code)
	}
	// Al descartar entre hojas la cola sigue contando: otro plazo u otro diagnóstico no es la misma lectura.
	for _, pair := range [][2]string{
		{"CONSULTA CONTROL NEUROLOGIA EN 1 MES", "CONSULTA CONTROL NEUROLOGIA EN 6 MESES"},
		{"EMG MMII CIE10 M545", "EMG MMII CIE10 G40"},
	} {
		if got := compareReadNames(pair[0], pair[1]); got == CupsNamesEqual {
			t.Errorf("compareReadNames(%q, %q) es igual: se descartaría una", pair[0], pair[1])
		}
	}
}
