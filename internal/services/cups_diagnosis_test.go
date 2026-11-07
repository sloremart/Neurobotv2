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
