package services

import (
	"slices"
	"strings"
	"testing"
)

// Revisión del lote C, ronda 7 (R7-C1, C2, M1): cortar el diagnóstico «hasta la primera palabra del
// procedimiento, conservándola» pegaba al procedimiento la región, el lado o la primera vez del diagnóstico
// («RX COLUMNA DORSAL DX DOLOR LUMBAR» → la dorsolumbar, por nombre igual), y cortaba como diagnóstico las palabras
// de tipo que el catálogo no trae tal cual (ANGIO, SEDACION, FLEXION). Ahora la cola tras DX se quita ENTERA solo
// si ninguna de sus palabras es vocabulario de procedimiento; si alguna lo es, duda con candidatos: nunca un
// descarte ni una asignación.
func TestResolveCupsByName_DiagnosisIsAllOrNothing(t *testing.T) {
	catalog := catalogFromTSV(t, "testdata/cups_catalog_active.tsv")
	doubts := []string{
		// R7-C1: región, lado o primera vez/control en el diagnóstico.
		"RX COLUMNA DORSAL DX DOLOR LUMBAR", "RX COLUMNA DORSAL DX: LUMBAR", "RX COLUMNA DORSAL DX DORSALGIA LUMBAR",
		"RX COLUMNA LUMBAR DX DOLOR DORSAL", "RX COLUMNA LUMBOSACRA DX DOLOR DORSAL", "TAC PELVIS DX DOLOR ABDOMINAL",
		"TAC ABDOMEN SUPERIOR DX DOLOR PELVICO", "TAC ABDOMEN DX DOLOR PELVICO", "RX MANO DX DOLOR DEDOS", "RX DEDOS DX DOLOR MANO",
		"RX HOMBRO IZQUIERDO DX DOLOR HOMBRO DERECHO", "RX RODILLA DERECHA DX GONARTROSIS IZQUIERDA", "CONSULTA NEUROLOGIA DX CRISIS PRIMERA VEZ",
		"RX PELVIS DX DOLOR COXO FEMORAL", "RX RODILLA DX GONARTROSIS D", "RX RODILLA DX GONARTROSIS OI", "RX COLUMNA LUMBAR DX DOLOR SACRO",
		"TAC CEREBRO DX MASTOIDITIS MASTOIDES", "RM CEREBRO DX TUMOR HIPOFISIS", "CONSULTA NEUROLOGIA DX NEUROPATIA PEDIATRICA",
		"CONSULTA NEUROLOGIA DX EPILEPSIA MAL CONTROLADA", "RM COLUMNA LUMBAR DX HERNIA L4 L5 CON CONTRASTE",
		// R7-C2: tipo o modalidad que el catálogo no trae tal cual.
		"RM CEREBRO DX ACV ANGIORESONANCIA", "RM CEREBRO DX ACV ANGIO", "RM CEREBRO DX ACV ANGIO RM", "TAC CRANEO DX ACV ANGIOTAC",
		"TAC CRANEO DX ACV ANGIOTOMOGRAFIA", "TAC TORAX DX TEP ANGIO", "RM CEREBRO DX EPILEPSIA BAJO SEDACION", "RM CEREBRO DX AUTISMO SEDACION",
		"RM CEREBRO DX AUTISMO BAJO ANESTESIA", "RX COLUMNA CERVICAL DX TRAUMA FLEXION EXTENSION", "RM CEREBRO DX EPILEPSIA PROTOCOLO EPILEPSIA",
		"RM COLUMNA LUMBAR DX HERNIA DINAMICA", "RX RODILLA DX GONARTROSIS COMPARATIVA", "RX RODILLA DX GONARTROSIS OBLICUAS",
		"RX PIES DX PIE PLANO CON APOYO", "ECO ABDOMEN DX DOLOR DOPPLER", "TAC TORAX DX EPID TACAR", "RM CEREBRO DX ACV DIFUSION",
		// Contraste tras el diagnóstico: no se asigna ni el simple ni el contrastado.
		"TAC CRANEO DX TCE CON CONTRASTE", "TAC CRANEO DX TCE SIN CONTRASTE", "TAC CRANEO DX TCE SIN FRACTURA", "TAC CRANEO DX TCE C/C",
		// 6b: palabras de contenido del catálogo, también en plural (MASA~MASAS).
		"RX RODILLA DX TUNEL", "RX RODILLA DX STRESS", "ECO ABDOMEN DX MASA", "NC DX TUNEL CARPIANO MMSS",
		// R7-M1: la consulta con el diagnóstico en medio no se descarta.
		"CONSULTA NEUROLOGIA CONTROL DX PRIMERA CRISIS CONVULSIVA", "CONSULTA DX CEFALEA CONTROL NEUROLOGIA", "RX RODILLA DX GONARTROSIS CONSULTA",
	}
	for _, read := range doubts {
		if got := acceptedByAdversary(read, catalog, false); len(got) != 0 {
			t.Errorf("%q → %v, quiero agente", read, got)
		}
		if res := ResolveCupsByName(read, catalog, false, nil); len(res.Candidates) == 0 {
			t.Errorf("%q: sin candidatos, se descartaría en silencio", read)
		}
	}
	// El diagnóstico solo, al final, se quita entero: la lectura se asigna como sin él. «DX,», «DX:» y «DX.» igual.
	for read, want := range map[string]string{
		"RX RODILLA DX GONARTROSIS": "873420", "RX RODILLA DERECHA DX GONARTROSIS": "873420", "RX RODILLA DX, GONARTROSIS": "873420",
		"RX RODILLA DX: GONARTROSIS": "873420", "RX RODILLA DX. GONARTROSIS": "873420", "TAC CRANEO CON CONTRASTE DX TCE": "879112",
		"TAC CRANEO SIMPLE DX TCE": "879111", "CONSULTA CONTROL NEUROLOGIA DX CEFALEA": "890374", "RM COLUMNA LUMBAR CON CONTRASTE DX HERNIA DISCAL": "883231",
		"EMG MMII DX LUMBALGIA": "930860", "EMG MMII DX RADICULOPATIA": "930860", "RX TORAX DX NEUMONIA": "871121", "TAC CRANEO DX TRAUMA": "879111",
	} {
		if got := acceptedByAdversary(read, catalog, false); !slices.Equal(got, []string{want}) {
			t.Errorf("%q → %v, quiero [%s]", read, got, want)
		}
	}
	// Los candidatos de la duda son los de lo anterior al DX; si la lectura empieza por DX, los de la entera.
	if res := ResolveCupsByName("DX GONARTROSIS RX RODILLA DERECHA", catalog, false, nil); res.Code != "" || !containsCode(res.Candidates, "873420") {
		t.Errorf("«DX GONARTROSIS RX RODILLA DERECHA» → %q / %v, quiero duda con 873420 entre los candidatos", res.Code, res.Candidates)
	}
}

// El vocabulario de procedimiento, pieza por pieza: contraste o negación, CONSULTA, valor de grupo, palabra del
// catálogo (igual, plural, prefijo), raíz de tipo; y lo que NO lo es (el diagnóstico corriente y los números).
func TestProcedureVocabulary(t *testing.T) {
	catalog := catalogFromTSV(t, "testdata/cups_catalog_active.tsv")
	vocabulary := procedureVocabulary(catalog)
	for _, w := range strings.Fields("SIN CON CONTRASTE SIMPLE CONSULTA LUMBAR RODILLA DERECHA L4 CONTROL PRIMERA PEDIATRICA RADIOGRAFIA " +
		"TUNEL STRESS MASA MASAS DORSALGIA CONTROLADA COMPARATIVA APOYO OBLICUAS ANGIO ANGIOTAC ANGIORESONANCIA SEDACION ANESTESIA FLEXION EXTENSION DINAMICA PROTOCOLO") {
		if !vocabulary(w) {
			t.Errorf("%q debe ser vocabulario de procedimiento", w)
		}
	}
	for _, w := range strings.Fields("DOLOR GONARTROSIS TCE CEFALEA HERNIA DISCAL LUMBALGIA EPILEPSIA TRAUMA FRACTURA TUMOR NODULO ACV TEP APNEA RADICULOPATIA CRISIS ESGUINCE NEUMONIA 2 123") {
		if vocabulary(w) {
			t.Errorf("%q no es vocabulario de procedimiento", w)
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
