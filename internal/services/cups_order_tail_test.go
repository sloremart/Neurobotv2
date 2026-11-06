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
	// Ronda 5b: las otras colas típicas que quedaban sin candidatos (SEGUN EVOLUCION, EPS <nombre>, CANT/CANTIDAD n,
	// n SESION(ES), DX <texto>, NO POS, ORDEN <n>) también son ruido al asignar.
	for _, read := range []string{
		"CONSULTA NEUROLOGIA SEGUN EVOLUCION", "CONSULTA NEUROLOGIA EPS SANITAS", "CONSULTA NEUROLOGIA CANT 1",
		"CONSULTA NEUROLOGIA CANTIDAD 1", "CONSULTA NEUROLOGIA 1 SESION", "EMG MMII DX LUMBALGIA",
		"EMG MMII SEGUN EVOLUCION", "EMG MMII NO POS", "EMG MMII EPS SANITAS", "EMG MMII ORDEN 123456",
		"EMG MMII CANTIDAD: 2", "CONSULTA NEUROLOGIA 2 SESIONES", "EMG MMII DX: LUMBALGIA MECANICA",
	} {
		if res := ResolveCupsByName(read, catalog, false, nil); len(res.Candidates) == 0 {
			t.Errorf("%q: sin candidatos, se descartaría en silencio", read)
		}
	}
	// Lo que queda tras la cola se asigna solo si encaja (como sin la cola).
	for read, want := range map[string][]string{
		"CONSULTA CONTROL NEUROLOGIA EN 3 MESES":   {"890374"},
		"CONSULTA NEUROLOGIA CONTROL CIE10 G40":    {"890374"},
		"EMG MMII POR DOLOR":                       {"930860"},
		"EMG MMII EN 6 MESES":                      {"930860"},
		"RM COLUMNA LUMBAR T12 L1 CIE10 M545":      {"883230"},
		"CONSULTA NEUROLOGIA EN 1 MES":             nil,
		"CONSULTA NEUROLOGIA POR CEFALEA":          nil,
		"EMG MMII DX LUMBALGIA":                    {"930860"},
		"EMG MMII NO POS":                          {"930860"},
		"CONSULTA CONTROL NEUROLOGIA EPS SANITAS":  {"890374"},
		"CONSULTA NEUROLOGIA CONTROL ORDEN 123456": {"890374"},
		"CONSULTA NEUROLOGIA SEGUN EVOLUCION":      nil,
	} {
		if got := acceptedByAdversary(read, catalog, false); !slices.Equal(got, want) {
			t.Errorf("%q → %v, quiero %v", read, got, want)
		}
	}
	// El PET de cuerpo entero es el PET (R6-M1): no se descarta en silencio.
	if got := acceptedByAdversary("PET CT CUERPO ENTERO FDG", catalog, false); !slices.Equal(got, []string{"921402"}) {
		t.Errorf("«PET CT CUERPO ENTERO FDG» → %v, quiero [921402]", got)
	}
	for _, read := range []string{"PET CT CUERPO ENTERO", "PET CUERPO COMPLETO FDG", "PET CT CUERPO ENTERO PSMA"} {
		if res := ResolveCupsByName(read, catalog, false, nil); len(res.Candidates) == 0 {
			t.Errorf("%q: sin candidatos, se descartaría en silencio", read)
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
		{"CONSULTA CONTROL NEUROLOGIA 1 SESION", "CONSULTA CONTROL NEUROLOGIA 2 SESIONES"},
		{"EMG MMII DX LUMBALGIA", "EMG MMII DX CERVICALGIA"},
		{"EMG MMII EPS SANITAS", "EMG MMII EPS SALUD TOTAL"},
	} {
		if got := compareReadNames(pair[0], pair[1]); got == CupsNamesEqual {
			t.Errorf("compareReadNames(%q, %q) es igual: se descartaría una", pair[0], pair[1])
		}
	}
}

// Revisión del lote C, ronda 6 (R6-C1, C2, I1): la cola de la 5b se pasaba. «DX …» hasta el final se comía el
// contraste escrito después («TAC CRANEO DX TCE CON CONTRASTE» → el simple) y la especialidad («CONSULTA DX
// CEFALEA CONTROL NEUROLOGIA» → descarte); «EPS <palabra>» se comía un SIN o un NO («TAC CRANEO EPS SIN
// CONTRASTE» → el contrastado por nombre igual). Ahora: EPS solo con la lista cerrada de EPS; DX corta hasta la
// primera palabra del procedimiento; el contraste y los grupos excluyentes se leen sobre la lectura COMPLETA, y si
// sin colas dicen otra cosa es duda; y si sin colas no hay candidatos se reintenta con la lectura entera.
func TestResolveCupsByName_OrderTailsNeverChangeTheMeaning(t *testing.T) {
	catalog := catalogFromTSV(t, "testdata/cups_catalog_active.tsv")
	simple := map[string]bool{"879111": true, "883230": true, "883210": true, "883101": true}
	contrasted := map[string]bool{"879112": true, "883231": true, "883211": true}
	// Con contraste tras DX o tras EPS: nunca el simple.
	for _, read := range []string{
		"TAC CRANEO DX TCE CON CONTRASTE", "TAC CRANEO DX: TCE. CON CONTRASTE", "RM COLUMNA LUMBAR DX HERNIA CON CONTRASTE",
		"RM COLUMNA DX LUMBALGIA LUMBAR CON CONTRASTE", "RM COLUMNA LUMBAR EPS CON CONTRASTE", "TAC CRANEO EPS CON CONTRASTE",
		"RM CEREBRO DX EPILEPSIA CON CONTRASTE", "TAC CRANEO PARA 2 DIAS CON CONTRASTE", "RM CEREBRO EN 1 MES CON CONTRASTE",
	} {
		for _, code := range acceptedByAdversary(read, catalog, false) {
			if simple[code] {
				t.Errorf("%q: se asignaría el SIMPLE %s", read, code)
			}
		}
	}
	// Sin contraste tras EPS o DX: nunca el contrastado.
	for _, read := range []string{
		"TAC CRANEO EPS SIN CONTRASTE", "TAC CRANEO EPS NO CONTRASTADO", "RM COLUMNA LUMBAR EPS SIN CONTRASTE",
		"TAC CRANEO SANITAS EPS SIN CONTRASTE", "RM COLUMNA LUMBAR DX HERNIA SIN CONTRASTE", "TAC CRANEO DX TCE SIN CONTRASTE",
		"TAC CRANEO EPS SIMPLE", "RM CEREBRO EPS SIMPLE",
	} {
		for _, code := range acceptedByAdversary(read, catalog, false) {
			if contrasted[code] {
				t.Errorf("%q: se asignaría el CONTRASTADO %s", read, code)
			}
		}
	}
	// Lo que se asigna bien: la cola quitada deja el procedimiento entero.
	for read, want := range map[string][]string{
		"TAC CRANEO DX TCE CON CONTRASTE":              {"879112"},
		"TAC CRANEO CON CONTRASTE DX TCE":              {"879112"},
		"TAC CRANEO SIMPLE DX TCE":                     {"879111"},
		"TAC CRANEO DX TCE SIN CONTRASTE":              {"879111"},
		"RM COLUMNA LUMBAR DX HERNIA CON CONTRASTE":    {"883231"},
		"RM COLUMNA DX LUMBALGIA LUMBAR CON CONTRASTE": {"883231"},
		"RX DX RODILLA DERECHA":                        {"873420"},
		"RX RODILLA DX GONARTROSIS DERECHA":            {"873420"},
		"CONSULTA DX CEFALEA CONTROL NEUROLOGIA":       {"890374"},
		"CONSULTA DX EPILEPSIA PRIMERA VEZ NEUROLOGIA": {"890274"},
		"CONSULTA NEUROLOGIA DX CEFALEA PRIMERA VEZ":   {"890274"},
		"EMG DX RADICULOPATIA MMII":                    {"930860"},
		// TUNEL es palabra de contenido de 873444: el corte tras DX se queda en duda (cerrado a fallos).
		"NC DX TUNEL CARPIANO MMSS":                     nil,
		"TAC CRANEO EPS SANITAS SIMPLE":                 {"879111"},
		"TAC CRANEO SANITAS EPS SIN CONTRASTE":          {"879111"},
		"CONSULTA NEUROLOGIA NUEVA EPS CONTROL":         {"890374"},
		"CONSULTA CONTROL NEUROLOGIA EPS SALUD TOTAL":   {"890374"},
		"CONSULTA CONTROL NEUROPEDIATRIA EPS FAMISANAR": {"890375"},
		"RX RODILLA IZQUIERDA EPS SANITAS":              {"873420"},
		"RM CEREBRO SIMPLE EPS CAPITAL SALUD":           {"883101"},
		// Una palabra tras EPS que no es una EPS de la lista se queda, y la lectura va al agente como antes.
		"TAC CRANEO EPS CON CONTRASTE": nil, "TAC CRANEO EPS SIN CONTRASTE": nil, "RX RODILLA EPS DERECHA": nil,
		"CONSULTA NEUROLOGIA EPS CONTROL": nil, "CONSULTA EPS PRIMERA VEZ NEUROLOGIA": nil,
		// Quitar la cola cambiaría lo que dice del contraste: duda, aunque sin ella encajara.
		"TAC CRANEO NO POS CONTRASTE": nil,
	} {
		if got := acceptedByAdversary(read, catalog, false); !slices.Equal(got, want) {
			t.Errorf("%q → %v, quiero %v", read, got, want)
		}
	}
	// Nada de esto se descarta en silencio: con duda, al agente con candidatos.
	for _, read := range []string{
		"CONSULTA DX CEFALEA CONTROL NEUROLOGIA", "CONSULTA DX EPILEPSIA PRIMERA VEZ NEUROLOGIA", "TAC CRANEO EPS SIN CONTRASTE",
		"CONSULTA NEUROLOGIA EPS CONTROL", "TAC CRANEO NO POS CONTRASTE", "CONSULTA NEUROLOGIA EPS SALUD TOTAL",
		"CONSULTA NEUROLOGIA EPS CAPITAL SALUD",
	} {
		if res := ResolveCupsByName(read, catalog, false, nil); len(res.Candidates) == 0 {
			t.Errorf("%q: sin candidatos, se descartaría en silencio", read)
		}
	}
	// Un nivel de dos cifras no es un código CIE (R6-M2): «COLUMNA CERVICAL T12» sigue siendo contradicción.
	for _, read := range []string{"RM COLUMNA CERVICAL T12", "RM COLUMNA CERVICAL D12", "RX COLUMNA CERVICAL T12 AP"} {
		if got := acceptedByAdversary(read, catalog, false); len(got) != 0 {
			t.Errorf("%q → %v, quiero agente (el nivel no cabe en la región)", read, got)
		}
	}
	if got := acceptedByAdversary("RM COLUMNA DORSAL T12", catalog, false); !slices.Equal(got, []string{"883220"}) {
		t.Errorf("«RM COLUMNA DORSAL T12» → %v, quiero [883220]", got)
	}
}

// Ronda 6, duda 3 (cerrado a fallos): si lo que se corta tras DX es una palabra de contenido del nombre de algún
// procedimiento del catálogo (TUNEL, STRESS, MASAS), puede que no fuera un diagnóstico: duda para un agente, nunca
// la asignación al nombre recortado. Un diagnóstico que no está en el catálogo (GONARTROSIS, TCE) sigue cortándose.
func TestResolveCupsByName_DiagnosisThatNamesAProcedureIsADoubt(t *testing.T) {
	catalog := catalogFromTSV(t, "testdata/cups_catalog_active.tsv")
	for _, read := range []string{"RX RODILLA DX TUNEL", "RX RODILLA DX: TUNEL", "RX RODILLA DX STRESS", "ECO DX MASA ABDOMEN", "NC DX TUNEL CARPIANO MMSS"} {
		if got := acceptedByAdversary(read, catalog, false); len(got) != 0 {
			t.Errorf("%q → %v, quiero agente", read, got)
		}
		if res := ResolveCupsByName(read, catalog, false, nil); len(res.Candidates) == 0 {
			t.Errorf("%q debe ir al agente con candidatos", read)
		}
	}
	for read, want := range map[string]string{
		"RX RODILLA DX GONARTROSIS": "873420", "RX RODILLA DX GONARTROSIS DERECHA": "873420", "TAC CRANEO DX TCE SIN CONTRASTE": "879111",
	} {
		if got := acceptedByAdversary(read, catalog, false); !slices.Equal(got, []string{want}) {
			t.Errorf("%q → %v, quiero [%s]", read, got, want)
		}
	}
}
