package services

import "testing"

// Revisión del lote C, ronda 3 (R3-I1, R3-I2): lo corto también distingue. Antes el comparador no veía las
// palabras de menos de 3 letras: «NO CONTRASTADO» se leía como «CONTRASTADO» y el lado, el nivel y la
// técnica escritos en corto no contaban.

// Un estudio negado nunca recibe el código con contraste, ni por nombre igual ni por el modelo, y la guarda
// de los CUPS con código lo ve como contradicción.
func TestResolveCupsByName_NegatedContrastIsSimple(t *testing.T) {
	const withContrast = "TOMOGRAFIA AXIAL COMPUTADA DE CRANEO CON CONTRASTE"
	for _, read := range []string{
		"TAC CRANEO NO CONTRASTADO", "TAC CRANEO NO CONTRASTADA", "TAC CRANEO NO CONTRASTE", "TAC CRANEO NO CON CONTRASTE",
		"TAC CRANEO NO MEDIO DE CONTRASTE", "TAC CRANEO NO CON MEDIO DE CONTRASTE", "TAC CRANEO SIN CONTRASTADO",
		"TAC CRANEO NO GADOLINIO", "TAC CRANEO SIN GADOLINIO", "TAC CRANEO NO C/C", "TAC CRANEO NO CON C/C",
		"TAC CRANEO CONTRASTE: NO", "TAC CRANEO CONTRASTE NO", "TAC CRANEO CONTRASTADO (NO)", "tac craneo no contrastado",
	} {
		if res := ResolveCupsByName(read, contrastCatalog, false, nil); res.Code != "879111" {
			t.Errorf("%q es el simple: %+v", read, res)
		}
		if !CupsNamesConflict(read, withContrast) {
			t.Errorf("%q contradice «%s»", read, withContrast)
		}
		if CupsNamesConflict(read, "TOMOGRAFIA AXIAL COMPUTADA DE CRANEO SIMPLE") {
			t.Errorf("%q no contradice el simple", read)
		}
	}
	// En otra columna, sin nombre SIMPLE igual, tampoco cae en el contrastado.
	for _, code := range acceptedByAdversary("RM COLUMNA LUMBAR NO CONTRASTADA", contrastCatalog, false) {
		if code == "883231" {
			t.Error("RM lumbar no contrastada: se asignaría la de contraste")
		}
	}
}

// Lo que está al lado de CONTRASTE sin negarlo no es una negación: «CONTRASTE NO IONICO» sí lleva contraste.
func TestResolveCupsByName_NonIonicContrastIsNotNegated(t *testing.T) {
	const read = "TAC CRANEO CON CONTRASTE NO IONICO"
	for _, code := range acceptedByAdversary(read, contrastCatalog, false) {
		if simpleCodes[code] {
			t.Errorf("%q: se asignaría el estudio SIMPLE %s", read, code)
		}
	}
	if res := ResolveCupsByName(read, contrastCatalog, false, nil); res.Code != "" {
		t.Errorf("%q es duda: %+v", read, res)
	}
	if !CupsNamesConflict(read, "TOMOGRAFIA AXIAL COMPUTADA DE CRANEO SIMPLE") {
		t.Errorf("%q contradice el simple", read)
	}
}

// «CON O SIN CONTRASTE» y «SIMPLE O CONTRASTADO» no dicen cuál: ni el simple, ni el contrastado, ni los dos.
func TestResolveCupsByName_ContrastEitherIsADoubt(t *testing.T) {
	for _, read := range []string{
		"TAC CRANEO CON O SIN CONTRASTE", "TAC CRANEO SIN O CON CONTRASTE", "TAC CRANEO SIMPLE O CONTRASTADO",
		"TAC CRANEO SIMPLE O CON CONTRASTE", "TAC CRANEO CON Y/O SIN CONTRASTE", "TAC CRANEO CON O SIN MEDIO DE CONTRASTE",
		"RM CEREBRO CON O SIN GADOLINIO",
	} {
		if got := acceptedByAdversary(read, contrastCatalog, false); len(got) != 0 {
			t.Errorf("%q: se asignaría %v", read, got)
		}
	}
}

// Un nombre cortado en «CON» no dice con qué: no vale como silencio del contraste (SIMPLE).
func TestResolveCupsByName_DanglingConIsADoubt(t *testing.T) {
	for _, read := range []string{"TAC CRANEO CON", "TAC CRANEO (CON)"} {
		if got := acceptedByAdversary(read, contrastCatalog, false); len(got) != 0 {
			t.Errorf("%q: se asignaría %v", read, got)
		}
	}
}

// Sitio específico frente a «no especificado»: el NO cuenta, también fuera del contraste.
func TestResolveCupsByName_NoCountsOutsideContrast(t *testing.T) {
	catalog := []CupsCandidate{{"883902", "RESONANCIA MAGNÉTICA DE SITIO NO ESPECIFICADO"}}
	if res := ResolveCupsByName("RM SITIO NO ESPECIFICADO", catalog, false, nil); res.Code != "883902" {
		t.Errorf("mismo nombre: %+v", res)
	}
	if got := acceptedByAdversary("RM SITIO ESPECIFICADO", catalog, false); len(got) != 0 {
		t.Errorf("«especificado» no es «no especificado»: %v", got)
	}
}

// Lado, nivel y técnica en corto se comparan como cualquier palabra.
func TestCompareReadNames_ShortWordsCount(t *testing.T) {
	cases := []struct {
		name, a, b string
		want       CupsNameMatch
	}{
		{"negación", "TAC CRANEO NO CONTRASTADO", "TAC CRANEO CONTRASTADO", CupsNamesDiffer},
		{"negación sin frase conocida", "TAC CRANEO NO CON CONTRASTE", "TAC CRANEO CON CONTRASTE", CupsNamesDiffer},
		{"negación igual a simple", "TAC CRANEO NO CONTRASTADO", "TAC CRANEO SIMPLE", CupsNamesEqual},
		{"negación igual a sin", "RM CEREBRO NO CONTRASTE", "RM CEREBRO SIN CONTRASTE", CupsNamesEqual},
		{"contraste dudoso", "TAC CRANEO SIMPLE O CONTRASTADO", "TAC CRANEO SIMPLE Y CONTRASTADO", CupsNamesDiffer},
		{"cortado en con", "TAC CRANEO CON", "TAC CRANEO", CupsNamesDiffer},

		{"lado en una letra", "RX RODILLA D", "RX RODILLA I", CupsNamesDiffer},
		{"lado entre paréntesis", "RX RODILLA (D)", "RX RODILLA (I)", CupsNamesDiffer},
		{"lado de la letra es la palabra", "RX RODILLA D", "RX RODILLA DERECHA", CupsNamesEqual},
		{"lado en tres letras", "RX RODILLA IZQ", "RX RODILLA I", CupsNamesEqual},
		{"lado en silencio", "RX RODILLA D", "RX RODILLA", CupsNamesByPrefix},
		{"miembro derecho e izquierdo", "RX MANO MD", "RX MANO MI", CupsNamesDiffer},
		{"ojo", "ECOGRAFIA OCULAR OD", "ECOGRAFIA OCULAR OI", CupsNamesDiffer},
		{"ojo igual a la palabra", "ECOGRAFIA OCULAR OD", "ECOGRAFIA OCULAR DERECHA", CupsNamesEqual},
		{"lados cruzados entre dos regiones", "RX RODILLA DERECHA Y CADERA IZQUIERDA", "RX RODILLA IZQUIERDA Y CADERA DERECHA", CupsNamesByPrefix},
		{"los dos lados en el mismo orden", "RX RODILLA DERECHA Y CADERA IZQUIERDA", "RADIOGRAFIA RODILLA DER CADERA IZQ", CupsNamesEqual},

		{"nivel", "RX COLUMNA L4 L5", "RX COLUMNA C5 C6", CupsNamesDiffer},
		{"nivel solo", "RM L4", "RM L5", CupsNamesDiffer},
		{"nivel en silencio", "RM COLUMNA T12", "RM COLUMNA", CupsNamesByPrefix},
		{"mismo nivel", "RX COLUMNA L4 L5", "RADIOGRAFIA DE COLUMNA L4-L5", CupsNamesEqual},

		{"técnica", "TAC 2D", "TAC 3D", CupsNamesDiffer},
		{"técnica de más", "TAC CRANEO 3D", "TAC CRANEO", CupsNamesDiffer},
		{"técnica separada", "TAC CRANEO 3 D", "TAC CRANEO 3D", CupsNamesEqual},
		{"duración", "EEG 6H", "EEG 8H", CupsNamesDiffer},
		{"duración separada", "EEG 6 H", "EEG 6H", CupsNamesEqual},
		{"cantidad pegada", "RX RODILLA X2", "RX RODILLA", CupsNamesByPrefix},
		{"cantidades distintas", "RX RODILLA #1", "RX RODILLA #4", CupsNamesByPrefix},
		{"cantidades en otra forma", "RX RODILLA X2", "RX RODILLA N 3", CupsNamesByPrefix},
		{"la misma cantidad", "RX RODILLA X2", "RX RODILLA #2", CupsNamesEqual},
		{"visita", "CONSULTA NEUROLOGIA 1V", "CONSULTA NEUROLOGIA 2V", CupsNamesDiffer},
		{"visita en silencio", "CONSULTA NEUROLOGIA PV", "CONSULTA NEUROLOGIA", CupsNamesDiffer},

		// Lo que de verdad es ruido sigue fuera.
		{"proyección en dos letras", "RX TORAX PA", "RX TORAX AP", CupsNamesEqual},
		{"proyección con puntos", "RX TORAX P.A.", "RX TORAX A.P.", CupsNamesEqual},
		{"proyección con puntos frente a nada", "RX TORAX P.A.", "RX TORAX", CupsNamesEqual},
		{"conectores", "RX DE LA RODILLA Y EL", "RX RODILLA", CupsNamesEqual},
		{"solo proyección no es procedimiento", "AP", "PA", CupsNamesDiffer},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := compareReadNames(c.a, c.b); got != c.want {
				t.Errorf("compareReadNames(%q, %q) = %d, quiero %d", c.a, c.b, got, c.want)
			}
			if got := compareReadNames(c.b, c.a); got != c.want {
				t.Errorf("compareReadNames(%q, %q) = %d, quiero %d", c.b, c.a, got, c.want)
			}
		})
	}
}

// Tal cual, sin pasar por la forma canónica (VALIDATE_OCR compara así el nombre leído con el catálogo).
func TestCompareCupsNames_ShortWordsCount(t *testing.T) {
	cases := []struct {
		a, b string
		want CupsNameMatch
	}{
		{"TOMOGRAFIA DE CRANEO NO CONTRASTADO", "TOMOGRAFIA DE CRANEO CONTRASTADO", CupsNamesDiffer},
		{"TOMOGRAFIA DE CRANEO NO CON CONTRASTE", "TOMOGRAFIA DE CRANEO CON CONTRASTE", CupsNamesDiffer},
		{"RADIOGRAFIA DE RODILLA D", "RADIOGRAFIA DE RODILLA I", CupsNamesDiffer},
		{"RESONANCIA DE COLUMNA L4", "RESONANCIA DE COLUMNA L5", CupsNamesDiffer},
		{"TOMOGRAFIA DE CRANEO 3D", "TOMOGRAFIA DE CRANEO", CupsNamesDiffer},
		{"RM CEREBRO", "CEREBRO", CupsNamesDiffer},
		{"ONDA F", "ONDA H", CupsNamesDiffer},
		{"RADIOGRAFIA DE TORAX AP", "RADIOGRAFIA DE TORAX P.A.", CupsNamesEqual},
		{"RADIOGRAFIA DE LA RODILLA", "RADIOGRAFIA RODILLA", CupsNamesEqual},
	}
	for _, c := range cases {
		if got := CompareCupsNames(c.a, c.b); got != c.want {
			t.Errorf("CompareCupsNames(%q, %q) = %v, quería %v", c.a, c.b, got, c.want)
		}
		if got := CompareCupsNames(c.b, c.a); got != c.want {
			t.Errorf("no es simétrico: CompareCupsNames(%q, %q) = %v", c.b, c.a, got)
		}
	}
}

// Lo corto se compara, pero no busca candidatos: si contara, «RM L4 L5» se quedaría sin ninguno y se
// descartaría en silencio, en vez de ir al agente con la lista.
func TestNameCandidates_ShortWordsDoNotLoseCandidates(t *testing.T) {
	catalog := []CupsCandidate{{"883230", "RESONANCIA NUCLEAR MAGNETICA DE COLUMNA LUMBOSACRA SIMPLE"}}
	for _, read := range []string{"RM L4 L5", "RM T12 L1 L2 L3", "RM D I", "RM OD"} {
		res := ResolveCupsByName(read, catalog, false, nil)
		if res.Code != "" || len(res.Candidates) == 0 {
			t.Errorf("%q debe quedar en duda con candidatos: %+v", read, res)
		}
	}
}

// Una abreviatura se comporta igual que su palabra: el lado en corto da el mismo resultado que el escrito.
func TestResolveCupsByName_ShortSideBehavesLikeTheWord(t *testing.T) {
	catalog := []CupsCandidate{{"873204", "RADIOGRAFIA DE HOMBRO"}, {"873420", "RADIOGRAFIA DE RODILLA AP, LATERAL"}}
	for _, region := range []string{"RX HOMBRO ", "RX RODILLA ", "RM RODILLA "} {
		for short, word := range map[string]string{"D": "DERECHA", "OD": "DERECHA", "MD": "DERECHA", "I": "IZQUIERDA", "OI": "IZQUIERDA", "MI": "IZQUIERDA"} {
			got := ResolveCupsByName(region+short, catalog, false, nil)
			want := ResolveCupsByName(region+word, catalog, false, nil)
			if got.Code != want.Code || len(got.Candidates) != len(want.Candidates) {
				t.Errorf("%q: %+v; con la palabra: %+v", region+short, got, want)
			}
			if a, b := acceptedByAdversary(region+short, catalog, false), acceptedByAdversary(region+word, catalog, false); len(a) != len(b) {
				t.Errorf("%q: el modelo acepta %v; con la palabra, %v", region+short, a, b)
			}
		}
	}
}

// Decisión del dueño: al ASIGNAR, el nivel de columna y la marca de cantidad son silencio si el catálogo no los
// menciona; si los menciona y es otro, es contradicción. Al descartar se siguen comparando (arriba).
func TestResolveCupsByName_SpineLevelAndCountAreSilence(t *testing.T) {
	catalog := []CupsCandidate{
		{"871040", "RADIOGRAFIA DE COLUMNA LUMBOSACRA"},
		{"871010", "RADIOGRAFIA DE COLUMNA CERVICAL"},
		{"883230", "RESONANCIA NUCLEAR MAGNETICA DE COLUMNA LUMBOSACRA SIMPLE"},
		{"883210", "RESONANCIA NUCLEAR MAGNETICA DE COLUMNA CERVICAL SIMPLE"},
		{"871121", "RADIOGRAFIA DE TORAX (P.A. Ó A.P.y LATERAL, DECUBITO LATERAL, OBLICUAS Ó LATERAL CON BARIO)"},
		{"873420", "RADIOGRAFIA DE RODILLA AP, LATERAL"},
	}
	for read, want := range map[string]string{
		"RX COLUMNA LUMBAR L4 L5": "871040", "RX COLUMNA LUMBAR L4-L5": "871040", "RX COLUMNA CERVICAL C5 C6": "871010",
	} {
		if res := ResolveCupsByName(read, catalog, false, nil); res.Code != want {
			t.Errorf("%q: %+v, quiero %s por nombre igual", read, res, want)
		}
	}
	for read, want := range map[string]string{
		"RM COLUMNA LUMBAR L4 L5": "883230", "RM COLUMNA CERVICAL C5 C6": "883210",
		"RX RODILLA X2": "873420", "RX TORAX N 2": "871121", "RX TORAX No. 1": "871121",
	} {
		got := acceptedByAdversary(read, catalog, false)
		if len(got) == 0 || got[0] != want {
			t.Errorf("%q: %v, quiero %s", read, got, want)
		}
	}
	const leveled = "RESONANCIA MAGNETICA DE COLUMNA L4 L5"
	if !CupsNamesConflict("RM COLUMNA L2 L3", leveled) {
		t.Error("otro nivel del que menciona el catálogo es contradicción")
	}
	if CupsNamesConflict("RM COLUMNA L4", leveled) || CupsNamesConflict("RM COLUMNA", leveled) {
		t.Error("el mismo nivel o el silencio no contradicen")
	}
	if CompareCupsNames("RESONANCIA MAGNETICA COLUMNA L2", leveled) != CupsNamesDiffer {
		t.Error("otro nivel no es el mismo nombre")
	}
	if got := acceptedByAdversary("RM COLUMNA L2 L3", []CupsCandidate{{"1", leveled}}, false); len(got) != 0 {
		t.Errorf("otro nivel no se asigna: %v", got)
	}
}
