package services

import (
	"bufio"
	"os"
	"strings"
	"testing"
)

// Ronda 2 del lote C (R2-I1): al conservar nombres de otras hojas solo se descarta lo estrictamente igual.
// Lo que encaja salvo un calificador en silencio es duda; sin palabra distintiva no hay con qué comparar.
func TestCompareReadNames_OnlyStrictlyEqualIsEqual(t *testing.T) {
	cases := []struct {
		name, a, b string
		want       CupsNameMatch
	}{
		{"misma redacción canónica", "RM CEREBRO", "RESONANCIA MAGNETICA DE CEREBRO", CupsNamesEqual},
		{"sigla y artículo", "TAC DE CRANEO SIMPLE", "TOMOGRAFIA COMPUTADA DE CRANEO SIMPLE", CupsNamesEqual},
		{"sinónimo", "RM ENCEFALO", "RESONANCIA MAGNETICA CEREBRAL", CupsNamesEqual},
		{"misma frase del contraste", "TAC CRANEO CON Y SIN CONTRASTE", "TAC CRANEO SIMPLE Y CONTRASTADO", CupsNamesEqual},
		{"ruido de prioridad", "RX TORAX URGENTE", "RADIOGRAFIA DE TORAX", CupsNamesEqual},
		{"proyección", "RX RODILLA AP Y LATERAL", "RX RODILLA", CupsNamesEqual},
		{"encabezado de remisión", "VALORACION NEUROLOGIA PRIMERA VEZ", "CONSULTA PRIMERA VEZ NEUROLOGIA", CupsNamesEqual},

		// Sin palabra distintiva en alguno de los dos no hay con qué comparar: nunca es igual ni duda.
		{"consulta frente a polisomnografía", "CONSULTA ESPECIALISTA", "POLISOMNOGRAFIA", CupsNamesDiffer},
		{"consulta frente a electromiografía", "CONSULTA ESPECIALISTA", "ELECTROMIOGRAFIA", CupsNamesDiffer},
		{"consulta frente a EEG", "CONSULTA ESPECIALISTA", "EEG", CupsNamesDiffer},
		{"consulta frente a tomografía", "CONSULTA", "TAC", CupsNamesDiffer},
		{"interconsulta frente a EEG", "INTERCONSULTA", "ELECTROENCEFALOGRAMA", CupsNamesDiffer},
		{"valoración frente a polisomnografía", "VALORACION", "POLISOMNOGRAFIA", CupsNamesDiffer},
		{"solo genéricas, aunque coincidan", "CONSULTA", "CONSULTA", CupsNamesDiffer},
		{"solo ruido", "URGENTE", "AMBULATORIO", CupsNamesDiffer},
		{"vacío", "", "RM CEREBRO", CupsNamesDiffer},

		// Distintos: un calificador explícito distinto o una modalidad explícita distinta.
		{"otra modalidad", "RX TORAX", "TAC TORAX", CupsNamesDiffer},
		{"con contraste frente a silencio", "RM CEREBRO", "RM CEREBRO CON CONTRASTE", CupsNamesDiffer},
		{"simple frente a contraste", "RM CEREBRO SIMPLE", "RM CEREBRO CON CONTRASTE", CupsNamesDiffer},
		{"lado distinto", "RM RODILLA DERECHA", "RM RODILLA IZQUIERDA", CupsNamesDiffer},
		{"primera vez y control", "CONSULTA PRIMERA VEZ NEUROLOGIA", "CONSULTA CONTROL NEUROLOGIA", CupsNamesDiffer},
		{"otra región", "RM CEREBRO", "RM RODILLA", CupsNamesDiffer},
		{"palabra de contenido de más", "ANGIOTAC CEREBRO", "TAC CEREBRO", CupsNamesDiffer},

		// Parecidos: no se descartan ni se asignan, van al agente.
		{"contraste frente a simple y contraste", "TAC CRANEO CON CONTRASTE", "TAC CRANEO SIMPLE Y CONTRASTADO", CupsNamesByPrefix},
		{"modalidad en silencio: tórax", "RX TORAX", "TORAX", CupsNamesByPrefix},
		{"modalidad en silencio: cráneo", "TAC CRANEO", "CRANEO", CupsNamesByPrefix},
		{"modalidad en silencio: abdomen", "TAC ABDOMEN", "ABDOMEN", CupsNamesByPrefix},
		{"modalidad en silencio: silla turca", "RX SILLA TURCA", "SILLA TURCA", CupsNamesByPrefix},
		{"contraste en silencio", "RM COLUMNA LUMBAR SIMPLE", "RNM COLUMNA LUMBAR", CupsNamesByPrefix},
		{"lado en silencio", "RX RODILLA", "RX RODILLA DERECHA", CupsNamesByPrefix},
		{"números en silencio", "EMG 4 EXTREMIDADES", "EMG", CupsNamesByPrefix},
		{"barra sin entender", "TAC C/X CRANEO", "TAC CRANEO", CupsNamesByPrefix},
		{"solo por prefijo", "EMG MIEMBRO INFERIOR", "EMG MIEMBROS INFERIORES", CupsNamesByPrefix},
		{"por prefijo con otro calificador en silencio", "RM MIEMBRO INFERIOR SIMPLE", "RM MIEMBROS INFERIORES", CupsNamesByPrefix},
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

// guardPair es una línea de testdata/cups_guard_pairs.txt.
type guardPair struct{ code, read, catalog, verdict string }

func readGuardPairs(t *testing.T) []guardPair {
	t.Helper()
	f, err := os.Open("testdata/cups_guard_pairs.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var pairs []guardPair
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		p := strings.Split(line, "|")
		pairs = append(pairs, guardPair{p[0], p[1], p[2], p[3]})
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if len(pairs) == 0 {
		t.Fatal("el archivo de pares está vacío")
	}
	return pairs
}

// Simulación con los pares reales: los marcados MAL son procedimientos distintos y nunca dan «igual».
func TestCompareReadNames_GuardPairsMarkedDistinctAreNeverEqual(t *testing.T) {
	distinct := 0
	for _, p := range readGuardPairs(t) {
		if p.verdict != "MAL" {
			continue
		}
		distinct++
		if got := compareReadNames(p.read, p.catalog); got == CupsNamesEqual {
			t.Errorf("%s: «%s» igual a «%s»", p.code, p.read, p.catalog)
		}
		if got := compareReadNames(p.catalog, p.read); got == CupsNamesEqual {
			t.Errorf("%s: «%s» igual a «%s»", p.code, p.catalog, p.read)
		}
	}
	if distinct == 0 {
		t.Fatal("el archivo no trae pares de distintos")
	}
}

// Simulación más dura sobre los mismos nombres, todos contra todos: los de catálogo de dos códigos
// distintos nunca son iguales, ni un nombre leído con el catálogo de otro código salvo que el archivo diga
// que ese leído también vale para ese código.
func TestCompareReadNames_AllGuardNamesOfOtherCodesAreNeverEqual(t *testing.T) {
	pairs := readGuardPairs(t)
	readsOf := map[string]map[string]bool{} // leído canónico → códigos para los que el archivo lo da por bueno
	for _, p := range pairs {
		if p.verdict != "OK" {
			continue
		}
		key := CupsNameKey(p.read)
		if readsOf[key] == nil {
			readsOf[key] = map[string]bool{}
		}
		readsOf[key][p.code] = true
	}
	// Las formas canónicas de cada nombre se calculan una vez (compareReadNames no cachea lecturas): son 9.000
	// comparaciones, y con -race cada canonización cuenta.
	catalogRow, readRow := make([]*catalogRow, len(pairs)), make([]*catalogRow, len(pairs))
	for i, p := range pairs {
		if p.verdict == "OK" {
			catalogRow[i], readRow[i] = newCatalogRow(p.catalog), newCatalogRow(p.read)
		}
	}
	checked := 0
	for i, x := range pairs {
		if x.verdict != "OK" {
			continue
		}
		validFor := readsOf[CupsNameKey(x.read)]
		for j, y := range pairs {
			if y.verdict != "OK" || x.code == y.code {
				continue
			}
			checked++
			if compareReadRows(catalogRow[i], catalogRow[j], x.catalog, y.catalog) == CupsNamesEqual {
				t.Errorf("catálogo: %s «%s» igual a %s «%s»", x.code, x.catalog, y.code, y.catalog)
			}
			if !validFor[y.code] && compareReadRows(readRow[i], catalogRow[j], x.read, y.catalog) == CupsNamesEqual {
				t.Errorf("leído %s «%s» igual al catálogo %s «%s»", x.code, x.read, y.code, y.catalog)
			}
		}
	}
	t.Logf("comparaciones entre códigos distintos: %d", checked)
}
