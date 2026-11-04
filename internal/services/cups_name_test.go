package services

import "testing"

// nameCatalog son procedimientos reales del catálogo (migrations/019).
var nameCatalog = []CupsCandidate{
	{"890264", "CONSULTA DE PRIMERA VEZ POR ESPECIALISTA EN MEDICINA FÍSICA Y REHABILITACIÓN"},
	{"890274", "CONSULTA DE PRIMERA VEZ POR ESPECIALISTA EN NEUROLOGÍA"},
	{"890275", "CONSULTA DE PRIMERA VEZ POR ESPECIALISTA EN NEUROLOGÍA PEDIÁTRICA"},
	{"890374", "CONSULTA DE CONTROL O DE SEGUIMIENTO POR ESPECIALISTA EN NEUROLOGÍA"},
	{"883101", "RESONANCIA NUCLEAR MAGNETICA DE CEREBRO"},
	{"883104", "RESONANCIA MAGNETICA DE CEREBRO FUNCIONAL"},
	{"883232", "RESONANCIA MAGNÉTICA DE ARTICULACIÓN SACROILÍACA SIMPLE"},
	{"883233", "RESONANCIA MAGNÉTICA DE ARTICULACIÓN SACROILÍACA CON CONTRASTE"},
	{"883511", "RESONANCIA MAGNETICA DE RODILLA"},
	{"881501", "ULTRASONOGRAFÍA DE PROSTATA TRANSABDOMINAL"},
	{"881502", "ULTRASONOGRAFÍA DE PROSTATA TRANSRECTAL"},
	{"921402", "TOMOGRAFIA POR EMISION DE POSITRONES(PET-TC) CON 18-FLUOR"},
	{"921405", "TOMOGRAFIA POR EMISION DE POSITRONES(PET-TC) CON PSMA"},
}

// chooseCode es un modelo guionizado: siempre devuelve code (con confianza alta).
func chooseCode(code string) func(string, []CupsCandidate) string {
	return func(string, []CupsCandidate) string { return code }
}

func TestResolveCupsByName_EqualNameIsAssigned(t *testing.T) {
	res := ResolveCupsByName("RM DE RODILLA", nameCatalog, false, nil)
	if res.Code != "883511" || res.Via != CupsNameViaEqual {
		t.Fatalf("un único nombre igual se asigna: %+v", res)
	}
	res = ResolveCupsByName("eco de próstata transrectal", nameCatalog, false, nil)
	if res.Code != "881502" {
		t.Fatalf("abreviatura y tildes: %+v", res)
	}
}

func TestResolveCupsByName_ModelHighConfidenceWithoutConflictAssigns(t *testing.T) {
	res := ResolveCupsByName("RNM CEREBRAL SIMPLE", nameCatalog, false, chooseCode("883101"))
	if res.Code != "883101" || res.Via != CupsNameViaModel {
		t.Fatalf("el modelo elige un candidato que encaja: %+v", res)
	}
}

// Un modelo que elige mal a propósito no pasa si el candidato contradice el nombre leído.
func TestResolveCupsByName_ModelChoiceThatContradictsIsADoubt(t *testing.T) {
	cases := []struct{ read, wrong string }{
		{"RNM CEREBRAL SIMPLE", "883104"},                    // FUNCIONAL que el leído no dice
		{"RM SACROILIACA CON CONTRASTE", "883232"},           // CONTRASTE frente a SIMPLE
		{"RM SACROILIACAS", "883233"},                        // el silencio es SIMPLE
		{"CONSULTA NEUROLOGIA PRIMERA VEZ ADULTO", "890374"}, // PRIMERA VEZ frente a CONTROL
		{"CONSULTA DE NEUROLOGIA 1A VEZ ADULTO", "890275"},   // PEDIÁTRICA que el leído no dice
		{"PET CT PSMA", "921402"},                            // marcador
		{"ECOGRAFIA PROSTATA VIA TRANSRECTAL", "881501"},     // vía
	}
	for _, c := range cases {
		if res := ResolveCupsByName(c.read, nameCatalog, false, chooseCode(c.wrong)); res.Code != "" {
			t.Errorf("%q: no puede asignarse %s: %+v", c.read, c.wrong, res)
		}
	}
}

func TestResolveCupsByName_LowConfidenceOrNoCandidatesIsADoubt(t *testing.T) {
	// El modelo devuelve "" cuando la confianza no es alta.
	if res := ResolveCupsByName("RNM CEREBRAL SIMPLE", nameCatalog, false, chooseCode("")); res.Code != "" || len(res.Candidates) == 0 {
		t.Errorf("sin confianza alta es duda, con candidatos para el agente: %+v", res)
	}
	if res := ResolveCupsByName("COLONOSCOPIA TOTAL", nameCatalog, false, chooseCode("883101")); res.Code != "" || len(res.Candidates) != 0 {
		t.Errorf("sin candidatos es duda: %+v", res)
	}
	// Un código fuera de la lista corta no se acepta.
	if res := ResolveCupsByName("RNM CEREBRAL SIMPLE", nameCatalog, false, chooseCode("890274")); res.Code != "" {
		t.Errorf("fuera de la lista: %+v", res)
	}
}

func TestResolveCupsByName_InterconsultaIsAFirstVisit(t *testing.T) {
	firstVisits := nameCatalog[:3]
	if res := ResolveCupsByName("NEUROLOGIA", firstVisits, true, nil); res.Code != "890274" {
		t.Errorf("interconsulta a neurología: %+v", res)
	}
	if res := ResolveCupsByName("Neuropediatría", firstVisits, true, nil); res.Code != "890275" {
		t.Errorf("interconsulta a neuropediatría: %+v", res)
	}
	if res := ResolveCupsByName("FISIATRIA", firstVisits, true, nil); res.Code != "890264" {
		t.Errorf("interconsulta a fisiatría: %+v", res)
	}
}

func TestNameCandidates_AtMostFiveBestFirst(t *testing.T) {
	got := nameCandidates(canonicalOrderName("RESONANCIA MAGNETICA"), nameCatalog)
	if len(got) != maxNameCandidates {
		t.Fatalf("lista corta de %d, quería %d", len(got), maxNameCandidates)
	}
	if got[0].Code != "883101" {
		t.Errorf("primero el de menos palabras de más: %+v", got[0])
	}
}

// La guarda de los CUPS con código: solo una contradicción EXPLÍCITA, nunca una abreviatura.
func TestCupsNamesConflict(t *testing.T) {
	cases := []struct {
		read, catalog string
		conflict      bool
	}{
		{"TAC CRANEO SIMPLE", "TOMOGRAFIA COMPUTADA DE CRANEO CON CONTRASTE", true},
		{"RM DE RODILLA", "RESONANCIA MAGNETICA DE HOMBRO", true},
		{"RX RODILLA DERECHA", "RADIOGRAFIA DE RODILLA IZQUIERDA", true},
		{"CONSULTA CONTROL NEUROLOGIA", "CONSULTA DE PRIMERA VEZ POR ESPECIALISTA EN NEUROLOGÍA", true},
		{"PET CT PSMA", "TOMOGRAFIA POR EMISION DE POSITRONES(PET-TC) CON 18-FLUOR", true},
		{"RM COLUMNA LUMBAR", "RESONANCIA MAGNETICA DE COLUMNA CERVICAL", true},
		{"RX DE MAXILAR INFERIOR", "RADIOGRAFIA DE MAXILAR SUPERIOR", true},
		// Abreviaturas y silencios: no contradicen.
		{"RNM CEREBRAL", "RESONANCIA NUCLEAR MAGNETICA DE CEREBRO", false},
		{"RM CEREBRO", "RESONANCIA MAGNETICA DE CEREBRO CON CONTRASTE", false},
		{"RX TORAX PA", "RADIOGRAFIA DE TORAX (P.A. O A.P. Y LATERAL)", false},
		{"EMG (#4) / 4 EXTREMIDADES", "ELECTROMIOGRAFIA EN CADA EXTREMIDAD", false},
		{"CONSULTA NEUROPEDIATRIA", "CONSULTA DE PRIMERA VEZ POR ESPECIALISTA EN NEUROLOGÍA PEDIÁTRICA", false},
		{"ECO PELVICA", "ULTRASONOGRAFIA PELVICA CON ANALISIS DOPPLER", false},
		{"", "RESONANCIA MAGNETICA DE RODILLA", false},
	}
	for _, c := range cases {
		if got := CupsNamesConflict(c.read, c.catalog); got != c.conflict {
			t.Errorf("CupsNamesConflict(%q, %q) = %v, quería %v", c.read, c.catalog, got, c.conflict)
		}
	}
}

// Si el nombre leído no distingue entre dos candidatos que encajan, la elección del modelo sería una
// adivinanza: es duda (simulación adversarial: «POTENCIALES VISUALES EVOCADOS» entre monofocales y
// multifocales; «CONSULTA PRIMERA VEZ» sin especialidad; paquetes que solo cambian en el número).
func TestResolveCupsByName_AmbiguousReadIsADoubtEvenIfTheModelChooses(t *testing.T) {
	catalog := []CupsCandidate{
		{"952302", "POTENCIALES VISUALES EVOCADOS MONOFOCALES"},
		{"952303", "POTENCIALES VISUALES EVOCADOS MULTIFOCALES"},
		{"000005", "Paq. 2 electromiografias + 2 neuroconducciones"},
		{"000006", "Paq. 1 electromiografia + 1 neuroconduccion"},
	}
	catalog = append(catalog, nameCatalog[:3]...)
	cases := []struct{ read, choice string }{
		{"POTENCIALES VISUALES EVOCADOS", "952302"},
		{"CONSULTA PRIMERA VEZ", "890274"},
		{"PAQ 1 EMG", "000005"},
	}
	for _, c := range cases {
		if res := ResolveCupsByName(c.read, catalog, false, chooseCode(c.choice)); res.Code != "" {
			t.Errorf("%q: ambiguo, no se asigna: %+v", c.read, res)
		}
	}
}

// Una interconsulta a una especialidad que el catálogo no tiene no tiene candidatos: el sufijo de
// primera vez no cuenta para buscarlos.
func TestResolveCupsByName_InterconsultaToAnotherSpecialtyHasNoCandidates(t *testing.T) {
	if res := ResolveCupsByName("OTORRINOLARINGOLOGIA", nameCatalog[:3], true, chooseCode("890274")); res.Code != "" || len(res.Candidates) != 0 {
		t.Errorf("sin candidatos: %+v", res)
	}
}

// La cantidad «(#N)» de la orden no es parte del nombre: no estorba al nombre igual.
func TestResolveCupsByName_IgnoresQuantityMark(t *testing.T) {
	if res := ResolveCupsByName("RM DE RODILLA (#2)", nameCatalog, false, nil); res.Code != "883511" {
		t.Errorf("con (#2): %+v", res)
	}
}

// La modalidad también distingue (simulación adversarial: «TAC SILLA TURCA» no es la radiografía de silla
// turca; «ECO VIAS URINARIAS» no es la urotomografía), y el doppler.
func TestResolveCupsByName_ModalityAndDopplerMustFit(t *testing.T) {
	catalog := []CupsCandidate{
		{"870004", "RADIOGRAFIA DE SILLA TURCA"},
		{"879430", "TOMOGRAFÍA COMPUTADA DE VÍAS URINARIAS [UROTC]"},
		{"881362", "ULTRASONOGRAFIA DE MASAS DE TEJIDOS BLANDOS DE ABDOMEN CON ANALISIS DOPPLER"},
	}
	cases := []struct{ read, choice string }{
		{"TAC SILLA TURCA", "870004"},
		{"ECO VIAS URINARIAS", "879430"},
		{"ECO TEJIDOS BLANDOS PARED ABDOMINAL", "881362"},
	}
	for _, c := range cases {
		if res := ResolveCupsByName(c.read, catalog, false, chooseCode(c.choice)); res.Code != "" {
			t.Errorf("%q: no puede asignarse %s: %+v", c.read, c.choice, res)
		}
	}
	if !CupsNamesConflict("TAC DE SILLA TURCA", "RADIOGRAFIA DE SILLA TURCA") {
		t.Error("guarda: TAC frente a radiografía es contradicción")
	}
	if CupsNamesConflict("PET CT CON FDG", "TOMOGRAFIA POR EMISION DE POSITRONES(PET-TC) CON 18-FLUOR") {
		t.Error("guarda: PET-CT es la misma modalidad que PET-TC")
	}
}

// Sin un único candidato que encaje no se llama al modelo (no hay nada que confirmar).
func TestResolveCupsByName_NoModelCallWhenNothingToConfirm(t *testing.T) {
	calls := 0
	choose := func(string, []CupsCandidate) string { calls++; return "952302" }
	catalog := []CupsCandidate{
		{"952302", "POTENCIALES VISUALES EVOCADOS MONOFOCALES"},
		{"952303", "POTENCIALES VISUALES EVOCADOS MULTIFOCALES"},
	}
	ResolveCupsByName("POTENCIALES VISUALES EVOCADOS", catalog, false, choose)
	if calls != 0 {
		t.Errorf("llamadas al modelo = %d, quería 0", calls)
	}
}
