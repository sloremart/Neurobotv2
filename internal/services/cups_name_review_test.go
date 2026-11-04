package services

import "testing"

// REVISIÓN DE LA ASIGNACIÓN POR NOMBRE (ronda 1). La simulación del revisor (nombres que el catálogo no
// trae, palabras compuestas, sinónimos y erratas) encontró asignaciones a OTRO procedimiento con un
// modelo adversarial. Aquí van como tests negativos, con los nombres reales del catálogo.

// reviewCatalog: procedimientos reales del catálogo activo (migrations/019).
var reviewCatalog = []CupsCandidate{
	{"883101", "RESONANCIA NUCLEAR MAGNETICA DE CEREBRO"},
	{"881141", "ULTRASONOGRAFIA DIAGNOSTICA DE TIROIDES CON TRANSDUCTOR DE 7 MHZ O MAS"},
	{"952301", "POTENCIALES EVOCADOS VISUALES (UNI O BILATERALES)"},
	{"881313", "ULTRASONOGRAFIA DE ABDOMEN: PILORO"},
	{"881302", "ULTRASONOGRAFÍA DE ABDOMEN TOTAL : HIGADO, PANCREAS, VESICULA, VIAS BILIARES, RIÑONES, BAZO, GRANDES VASOS, PELVIS Y FLANCOS"},
	{"873004", "RADIOGRAFIA PARA DETECTAR EDAD OSEA [CARPOGRAMA]"},
	{"930860", "ELECTROMIOGRAFÍIA EN CADA EXTREMIDAD (UNO O MAS MUSCULOS)"},
	{"930801", "ELECTROMIOGRAFÍA CON ELECTRODO DE FIBRA ÚNICA"},
	{"930820", "ELECTROMIOGRAFÍA EN CARA"},
	{"053105", "BLOQUEO DE UNION MIONEURAL  (65)"},
	{"871019", "RADIOGRAFIA DE COLUMNA UNION CERVICO DORSAL"},
	{"871010", "RADIOGRAFIA DE COLUMNA CERVICAL"},
	{"871020", "RADIOGRAFIA DE COLUMNA TORACICA"},
	{"879301", "TOMOGRAFIA AXIAL COMPUTADA DE TORAX"},
	{"881431", "ULTRASONOGRAFÍA OBSTETRICA TRANSABDOMINAL"},
	{"881432", "ULTRASONOGRAFÍA OBSTETRICA TRANSVAGINAL"},
	{"954626", "POTENCIALES EVOCADOS AUDITIVOS DE CORTA LATENCIA MEDICIÓN DE INTEGRIDAD"},
	{"891509", "NEUROCONDUCCION (CADA NERVIO)"},
	{"890364", "CONSULTA DE CONTROL O DE SEGUIMIENTO POR ESPECIALISTA EN MEDICINA FISICA Y REHABILITACION"},
	{"890374", "CONSULTA DE CONTROL O DE SEGUIMIENTO POR ESPECIALISTA EN NEUROLOGÍA"},
	{"890274", "CONSULTA DE PRIMERA VEZ POR ESPECIALISTA EN NEUROLOGÍA"},
	{"882308", "ECOGRAFÍA DOPPLER DE VASOS ARTERIALES DE MIEMBROS INFERIORES"},
	{"921402", "TOMOGRAFIA POR EMISION DE POSITRONES(PET-TC) CON 18-FLUOR"},
	{"879901", "TOMOGRAFÍA COMPUTADA DE VASOS"},
}

// acceptedByAdversary devuelve los códigos que se aceptarían si el modelo eligiera, uno por uno, cada
// candidato de la lista corta (el peor caso: un modelo que se equivoca siempre que puede).
func acceptedByAdversary(read string, catalog []CupsCandidate, interconsulta bool) []string {
	var accepted []string
	for try := 0; try < maxNameCandidates; try++ {
		res := ResolveCupsByName(read, catalog, interconsulta, func(_ string, cands []CupsCandidate) string {
			if try < len(cands) {
				return cands[try].Code
			}
			return ""
		})
		if res.Code != "" {
			accepted = append(accepted, res.Code)
		}
		if res.Via == CupsNameViaEqual {
			break
		}
	}
	return accepted
}

// Los 15 de la revisión (y los de la misma familia): nunca se asignan a otro procedimiento.
func TestResolveCupsByName_ReviewerCasesNeverAssignAnotherProcedure(t *testing.T) {
	for _, read := range []string{
		"ANGIOTAC CEREBRAL", "ANGIORESONANCIA CEREBRAL", "ANGIO RM CEREBRAL", "VENORESONANCIA CEREBRAL",
		"GAMAGRAFIA DE TIROIDES", "MAMOGRAFIA BILATERAL", "ECO ABDOMEN TOTAL", "DENSITOMETRIA OSEA",
		"BIOPSIA DE MUSCULO", "BLOQUEO NEUROLITICO", "RX COLUMNA CERVICAL DINAMICA",
		"TAC DE TORAX DE ALTA RESOLUCION", "ECO TRANSVAGINAL", "POTENCIALES EVOCADOS AUDITIVOS",
		// Procedimientos que el catálogo no tiene y palabras compuestas.
		"UROTAC", "COLANGIORESONANCIA", "ARTRORESONANCIA DE RODILLA", "TACAR", "ECOCARDIOGRAMA",
		"RESONANCIA MAGNETICA DE CEREBRO CON ESPECTROSCOPIA", "RM CEREBRO PROTOCOLO EPILEPSIA",
		"EMG DE FIBRA UNICA DE CARA",
	} {
		if got := acceptedByAdversary(read, reviewCatalog, false); len(got) != 0 {
			t.Errorf("%q: se asignaría a otro procedimiento: %v", read, got)
		}
	}
}

// Con un único candidato correcto, el adversario no puede colar otro: ELECTROMIOGRAFÍA a secas es la de
// cada extremidad (la errata «ELECTROMIOGRAFÍIA» del catálogo es un alias) y RX COLUMNA DORSAL es la
// torácica, no la unión cervicodorsal.
func TestResolveCupsByName_AdversaryCanOnlyConfirmTheRightOne(t *testing.T) {
	cases := map[string]string{
		"ELECTROMIOGRAFIA":            "930860",
		"RX COLUMNA DORSAL":           "871020",
		"CONSULTA CONTROL NEUROLOGIA": "890374",
		"DOPPLER ARTERIAL MMII":       "882308",
		"NEUROCONDUCCIONES MMII":      "891509",
		"PET CT CON FDG":              "921402",
	}
	for read, want := range cases {
		got := acceptedByAdversary(read, reviewCatalog, false)
		for _, code := range got {
			if code != want {
				t.Errorf("%q: se asignaría %s, solo vale %s", read, code, want)
			}
		}
		if len(got) == 0 {
			t.Errorf("%q: el correcto (%s) debería poder confirmarse", read, want)
		}
	}
}

// Guarda 4: solo los grupos que de verdad se excluyen; los sinónimos de región no son contradicción.
func TestCupsNamesConflict_SynonymsAreNotAContradiction(t *testing.T) {
	cases := []struct{ read, catalog string }{
		{"TAC CEREBRAL SIMPLE", "TOMOGRAFIA AXIAL COMPUTADA DE CRANEO SIMPLE"},
		{"TAC DE CEREBRO", "TOMOGRAFIA AXIAL COMPUTADA DE CRANEO SIMPLE"},
		{"TAC CEREBRAL CONTRASTADO", "TOMOGRAFIA AXIAL COMPUTADA DE CRANEO CON CONTRASTE"},
		{"RM CRANEO SIMPLE", "RESONANCIA NUCLEAR MAGNETICA DE CEREBRO"},
		{"RESONANCIA MAGNETICA DE CRANEO", "RESONANCIA NUCLEAR MAGNETICA DE CEREBRO"},
		{"RNM COLUMNA LUMBAR SIMPLE", "RESONANCIA NUCLEAR MAGNETICA DE COLUMNA LUMBOSACRA SIMPLE"},
		{"RM DE COLUMNA LUMBAR", "RESONANCIA NUCLEAR MAGNETICA DE COLUMNA LUMBOSACRA SIMPLE"},
		{"RNM COLUMNA LUMBOSACRA CON CONTRASTE", "RESONANCIA MAGNETICA DE COLUMNA LUMBAR CON CONTRASTE"},
		{"RX COLUMNA LUMBAR", "RADIOGRAFIA DE COLUMNA LUMBOSACRA"},
		{"RX CADERA", "RADIOGRAFIA DE PELVIS O ARTICULACION COXO-FEMORAL (AP, LATERAL )"},
		{"RM HIPOFISIS", "RESONANCIA NUCLEAR MAGNETICA DE BASE DE CRANEO-SILLA TURCA"},
		{"RM COLUMNA TORACICA", "RESONANCIA NUCLEAR MAGNETICA DE COLUMNA TORACICA SIMPLE"},
		{"TAC DE SILLA TURCA", "RADIOGRAFIA DE SILLA TURCA"}, // la modalidad no es un grupo excluyente de la guarda
	}
	for _, c := range cases {
		if CupsNamesConflict(c.read, c.catalog) {
			t.Errorf("CupsNamesConflict(%q, %q) = true: un sinónimo no es contradicción", c.read, c.catalog)
		}
	}
	if !CupsNamesConflict("CONSULTA NEUROLOGIA ADULTOS", "CONSULTA DE PRIMERA VEZ POR ESPECIALISTA EN NEUROLOGÍA PEDIÁTRICA") {
		t.Error("pediátrica frente a adultos sí es contradicción")
	}
}
