package services

import (
	"bufio"
	"os"
	"strings"
	"testing"
)

// Revisión del lote C, ronda 8 (decisión definitiva del dueño): una lectura con el diagnóstico escrito junto al
// nombre NUNCA se asigna, ni por nombre igual ni por el modelo. Cuatro rondas de cortar el diagnóstico para asignar
// dejaron escapar cada vez una variante (ANGIO, FLEXOEXTENSION, IV, YODADO, ALTA RES, AMBAS…) y la lectura iba a
// otro procedimiento. La marca sirve solo para que la duda llegue al agente con candidatos.
func TestResolveCupsByName_DiagnosisIsAlwaysADoubt(t *testing.T) {
	catalog := catalogFromTSV(t, "testdata/cups_catalog_active.tsv")
	// Las sondas del revisor (R8-C1, I1, I2 y las variantes de la marca), todas con diagnóstico escrito.
	for _, path := range []string{"testdata/cups_diagnosis_probes.txt"} {
		for _, read := range linesOf(t, path) {
			if got := acceptedByAdversary(read, catalog, false); len(got) != 0 {
				t.Errorf("%q → %v, quiero agente", read, got)
			}
			if res := ResolveCupsByName(read, catalog, false, nil); len(res.Candidates) == 0 {
				t.Errorf("%q: sin candidatos, se descartaría en silencio", read)
			}
		}
	}
	// Lo que en la ronda 7 se asignaba con el diagnóstico al final también es duda ahora.
	for _, read := range []string{
		"RX RODILLA DERECHA DX GONARTROSIS", "EMG MMII DX LUMBALGIA", "TAC CRANEO CON CONTRASTE DX TCE", "TAC CRANEO SIMPLE DX TCE",
		"CONSULTA CONTROL NEUROLOGIA DX CEFALEA", "RM COLUMNA LUMBAR CON CONTRASTE DX HERNIA DISCAL", "RX TORAX DX NEUMONIA",
		"RX RODILLA DX GONARTROSIS", "RX RODILLA DX, GONARTROSIS", "RX RODILLA DX: GONARTROSIS", "RX RODILLA DX. GONARTROSIS",
		"RX RODILLA Dx GONARTROSIS", "RX RODILLA DX:GONARTROSIS", "RX RODILLA (DX GONARTROSIS)", "RX RODILLA D/X GONARTROSIS",
		"RX RODILLA IDX GONARTROSIS", "RX RODILLA DIAG GONARTROSIS", "RX RODILLA DIAGNOSTICO: GONARTROSIS",
		"RX RODILLA IMPRESION DX GONARTROSIS", "RX RODILLA IMPRESION DIAGNOSTICA GONARTROSIS",
	} {
		if got := acceptedByAdversary(read, catalog, false); len(got) != 0 {
			t.Errorf("%q → %v, quiero agente", read, got)
		}
		if res := ResolveCupsByName(read, catalog, false, nil); len(res.Candidates) == 0 {
			t.Errorf("%q: sin candidatos, se descartaría en silencio", read)
		}
	}
	// Los candidatos: los de lo anterior a la marca; si no hay, los de la lectura entera; si no, los del diagnóstico.
	for read, want := range map[string]string{
		"RX RODILLA DERECHA DX GONARTROSIS": "873420", "DX CEFALEA RM CEREBRO": "883101", "MAMOGRAFIA DX NODULO MAMA IZQUIERDA": "881201",
	} {
		if res := ResolveCupsByName(read, catalog, false, nil); res.Code != "" || !containsCode(res.Candidates, want) {
			t.Errorf("%q → %q / %v, quiero duda con %s entre los candidatos", read, res.Code, res.Candidates, want)
		}
	}
	// Sin nada que buscar en ninguna de las tres formas, queda como la orden sin candidatos.
	if res := ResolveCupsByName("DX CEFALEA", catalog, false, nil); res.Code != "" || len(res.Candidates) != 0 {
		t.Errorf("«DX CEFALEA» → %q / %v, quiero sin candidatos", res.Code, res.Candidates)
	}
	// DIAGNOSTICA sola es palabra del catálogo, no una marca; y «DX» dentro de una palabra tampoco lo es.
	for _, read := range []string{"ULTRASONOGRAFIA DIAGNOSTICA DE TEJIDOS BLANDOS DE PARED ABDOMINAL Y DE PELVIS", "ECOGRAFIA DE TIROIDES"} {
		if _, _, found := splitDiagnosis(prefoldProcedureText(read)); found {
			t.Errorf("%q no lleva marca de diagnóstico", read)
		}
	}
}

// R8-M5: el invariante de las colas sin diagnóstico: una cola que al quitarse cambia lo que la lectura dice del
// contraste («NO POS» delante del contraste) deja la lectura en duda, con candidatos, nunca asignada.
func TestResolveCupsByName_InvariantWithoutDiagnosis(t *testing.T) {
	catalog := catalogFromTSV(t, "testdata/cups_catalog_active.tsv")
	for _, read := range []string{"TAC CRANEO NO POS CONTRASTE", "RM COLUMNA LUMBAR NO POS CON CONTRASTE", "TAC CRANEO NO POS CONTRASTADO"} {
		if got := acceptedByAdversary(read, catalog, false); len(got) != 0 {
			t.Errorf("%q → %v, quiero agente", read, got)
		}
		if res := ResolveCupsByName(read, catalog, false, nil); len(res.Candidates) == 0 {
			t.Errorf("%q: sin candidatos", read)
		}
	}
}

// Ronda 6 (d): sin candidatos tras quitar las colas, se reintenta con la lectura entera antes de descartar.
// Catálogo sintético: la cola («10 SESIONES») es lo único que casa con la fila.
func TestResolveCupsByName_RetriesWithTheWholeReadBeforeDiscarding(t *testing.T) {
	catalog := []CupsCandidate{{"X1", "TERAPIA 10 SESIONES"}}
	if res := ResolveCupsByName("ZUMBA 10 SESIONES", catalog, false, nil); len(res.Candidates) == 0 {
		t.Error("«ZUMBA 10 SESIONES»: sin la cola no hay candidatos y con la lectura entera sí; se descartaría en silencio")
	}
}

// linesOf lee las lecturas de un corpus de testdata (una por línea; «#» comenta).
func linesOf(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" && !strings.HasPrefix(line, "#") {
			out = append(out, line)
		}
	}
	return out
}

// Ronda 9 (R9-I1, R9-I2): con el diagnóstico largo DELANTE del procedimiento («DX: RADICULOPATIA LUMBAR CRONICA
// EMG MMII») la lectura entera no llegaba a minCandidateScore y se descartaba; y la marca pegada a la puntuación
// («EMG MMII.DX:»), con IMP./IMPRESION, en plural o numerada no se reconocía. Los candidatos se buscan por separado en
// lo anterior, lo posterior y cada prefijo y sufijo de lo posterior; y cualquier signo separa la marca.
func TestResolveCupsByName_DiagnosisBeforeTheProcedureKeepsCandidates(t *testing.T) {
	catalog := catalogFromTSV(t, "testdata/cups_catalog_active.tsv")
	for _, line := range linesOf(t, "testdata/cups_diagnosis_before_probes.txt") {
		read := strings.ReplaceAll(line, `\n`, "\n")
		if got := acceptedByAdversary(read, catalog, false); len(got) != 0 {
			t.Errorf("%q → %v, quiero agente", read, got)
		}
		if res := ResolveCupsByName(read, catalog, false, nil); len(res.Candidates) == 0 {
			t.Errorf("%q: sin candidatos, se descartaría en silencio", read)
		}
	}
	// El estudio escrito tras el diagnóstico está entre los candidatos.
	for read, want := range map[string]string{
		"DX: RADICULOPATIA LUMBAR CRONICA EMG MMII": "930860", "DX: SINDROME DE TUNEL DEL CARPO BILATERAL NC MMSS": "891509",
		"DX PRINCIPAL: LUMBALGIA CRONICA MECANICA TAC CRANEO SIMPLE": "879111", "DX1: EPILEPSIA DX2: CEFALEA RM CEREBRO SIMPLE": "883101",
		"EMG MMII.DX: RADICULOPATIA LUMBAR CRONICA": "930860", "RM CEREBRO SIMPLE DIAGNOSTICOS: EPILEPSIA, CEFALEA": "883101",
	} {
		if res := ResolveCupsByName(read, catalog, false, nil); res.Code != "" || !containsCode(res.Candidates, want) {
			t.Errorf("%q → %q / %v, quiero duda con %s entre los candidatos", read, res.Code, res.Candidates, want)
		}
	}
	// La marca y sus separadores; y lo que no es marca (DXA, ELECTRODIAGNOSTICO, DIAGNOSTICA sola).
	for read, head := range map[string]string{
		"EMG MMII.DX: RADICULOPATIA": "EMG MMII", "RM CEREBRO SIMPLE,DX: EPILEPSIA": "RM CEREBRO SIMPLE", "RM CEREBRO SIMPLE -DX: EPILEPSIA": "RM CEREBRO SIMPLE",
		"RM CEREBRO SIMPLE [DX EPILEPSIA": "RM CEREBRO SIMPLE", "EMG MMII IMP. DX: RADICULOPATIA": "EMG MMII", "EMG MMII IMP DX TCE": "EMG MMII",
		"EMG MMII IMP. DIAGNOSTICA: RADICULOPATIA": "EMG MMII", "RM CEREBRO SIMPLE DIAGNOSTICOS: EPILEPSIA": "RM CEREBRO SIMPLE",
		"RM CEREBRO SIMPLE DX1: EPILEPSIA FOCAL DX2: CEFALEA": "RM CEREBRO SIMPLE", "DX2: CEFALEA RM CEREBRO": "",
	} {
		got, _, found := splitDiagnosis(prefoldProcedureText(read))
		if !found || got != head {
			t.Errorf("splitDiagnosis(%q) = %q/%v, quiero %q", read, got, found, head)
		}
	}
	for _, read := range []string{"DENSITOMETRIA DXA", "ELECTRODIAGNOSTICO MMII", "ECO DIAGNOSTICA TIROIDES", "ULTRASONOGRAFIA DIAGNOSTICA DE TEJIDOS BLANDOS"} {
		if _, _, found := splitDiagnosis(prefoldProcedureText(read)); found {
			t.Errorf("%q no lleva marca de diagnóstico", read)
		}
	}
}

// R10-M1 y M3: con el diagnóstico LARGO, antes o después del procedimiento, el estudio escrito en la orden va el
// primero entre los candidatos que ve el agente (los trozos de lo posterior a la marca se recorren de más largo a más
// corto, y primero los que encajan). Nunca un código.
func TestDiagnosisCandidates_LongestSegmentsFirst(t *testing.T) {
	catalog := catalogFromTSV(t, "testdata/cups_catalog_active.tsv")
	for read, want := range map[string]string{
		"RX RODILLA DERECHA DX: LUMBALGIA CRONICA MECANICA DEGENERATIVA SEVERA":                             "873420",
		"DX: LUMBALGIA CRONICA MECANICA DEGENERATIVA SEVERA RX RODILLA DERECHA":                             "873420",
		"DX: RADICULOPATIA LUMBAR CRONICA BILATERAL L5 S1 SEVERA EMG MMII":                                  "930860",
		"DIAGNOSTICO: TRAUMA CRANEOENCEFALICO LEVE CON PERDIDA DE CONCIENCIA TAC CRANEO SIMPLE":             "879111",
		"IMPRESION DIAGNOSTICA: EPILEPSIA FOCAL REFRACTARIA AL TRATAMIENTO FARMACOLOGICO RM CEREBRO SIMPLE": "883101",
		"DX1: CEFALEA CRONICA DIARIA DX2: TRASTORNO DEL SUENO CONSULTA NEUROLOGIA PRIMERA VEZ":              "890274",
		"DX: SINDROME DE TUNEL DEL CARPO BILATERAL SEVERO DE LARGA DATA NC MMSS":                            "891509",
		// El ascenso (R10-I2): «RADICULOPATIA EMG» entero trae todas las filas de EMG por parecido, y la de cada
		// extremidad (930860, seis palabras) queda la última, fuera de los cinco; el trozo «EMG» la hace encajar y
		// sube la primera. Sin el ascenso se quedaría sexta.
		"DX: RADICULOPATIA EMG": "930860", "DX RADICULOPATIA EMG": "930860", "DIAGNOSTICO: RADICULOPATIA EMG": "930860",
		// R10-M3: el procedimiento justo tras la marca y el diagnóstico largo detrás: solo los prefijos de lo
		// posterior lo traen.
		"DX: EMG MMII RADICULOPATIA LUMBAR CRONICA BILATERAL SEVERA":                   "930860",
		"DX: RX RODILLA DERECHA GONARTROSIS SEVERA BILATERAL DEGENERATIVA":             "873420",
		"DX: TAC CRANEO SIMPLE TRAUMA CRANEOENCEFALICO LEVE CON PERDIDA DE CONCIENCIA": "879111",
		"DX: NC MMSS SINDROME DE TUNEL DEL CARPO BILATERAL SEVERO DE LARGA DATA":       "891509",
	} {
		res := ResolveCupsByName(read, catalog, false, nil)
		if res.Code != "" || len(res.Candidates) == 0 || res.Candidates[0].Code != want {
			t.Errorf("%q → %q / %v, quiero duda con %s el primero", read, res.Code, res.Candidates, want)
		}
	}
	// El orden fijado (R10-I1): lo posterior entero trae A y B por parecido (ninguna encaja: dos regiones), en el
	// orden del catálogo (B antes que A). De más largo a más corto, «RX RODILLA DERECHA RX» hace encajar a A antes de
	// que «DERECHA RX TOBILLO» haga encajar a B, y el ascenso las saca de «parecido»: [A B]. Con el orden intercalado
	// por posición, o sin el ascenso, saldría [B A].
	synthetic := []CupsCandidate{{"B", "RADIOGRAFIA DE TOBILLO"}, {"A", "RADIOGRAFIA DE RODILLA"}}
	got := diagnosisCandidates("", "RX RODILLA DERECHA RX TOBILLO", synthetic)
	if len(got) != 2 || got[0].Code != "A" || got[1].Code != "B" {
		t.Errorf("diagnosisCandidates = %v, quiero [A B]", got)
	}
}
