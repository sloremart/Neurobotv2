package services

import (
	"bufio"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
	"testing"
)

// catalogFromTSV lee un catálogo código<TAB>nombre de testdata («\n» en el nombre es un salto de línea).
func catalogFromTSV(t *testing.T, path string) []CupsCandidate {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var out []CupsCandidate
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		code, name, ok := strings.Cut(line, "\t")
		if !ok {
			t.Fatalf("línea sin tabulador en %s: %q", path, line)
		}
		out = append(out, CupsCandidate{strings.TrimSpace(code), strings.ReplaceAll(name, `\n`, "\n")})
	}
	return out
}

// Revisión del lote C, ronda 5 (R5-C2): la guarda es la única red contra un dígito mal leído en un CUPS con
// código, y los hermanos del catálogo están justo a un dígito. Barrido catálogo × catálogo: para cada par de
// códigos a un dígito (CupsOneDigitApart), el nombre exacto de uno con el código del otro. Los pares de
// testdata/cups_one_digit_conflicts.txt son los que la guarda ve (los de 6452bfd más los ganados después):
// ninguno puede perderse. Regenerar el archivo tras una ganancia a propósito:
//
//	CUPS_SWEEP_WRITE=1 go test ./internal/services -run TestCupsNamesConflict_OneDigitSiblingsSweep
func TestCupsNamesConflict_OneDigitSiblingsSweep(t *testing.T) {
	catalog := catalogFromTSV(t, "testdata/cups_catalog_active.tsv")
	conflicts := map[string]bool{}
	pairs := 0
	for _, a := range catalog {
		for _, b := range catalog {
			if !CupsOneDigitApart(a.Code, b.Code) {
				continue
			}
			pairs++
			if CupsNamesConflict(a.Name, b.Name) {
				conflicts[a.Code+"->"+b.Code] = true
			}
		}
	}
	const golden = "testdata/cups_one_digit_conflicts.txt"
	if os.Getenv("CUPS_SWEEP_WRITE") == "1" {
		keys := make([]string, 0, len(conflicts))
		for k := range conflicts {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		header := "# Pares de códigos a un dígito (leído->código) cuyos nombres exactos la guarda (CupsNamesConflict) ve como\n" +
			"# contradicción. Generado por TestCupsNamesConflict_OneDigitSiblingsSweep con CUPS_SWEEP_WRITE=1.\n"
		if err := os.WriteFile(golden, []byte(header+strings.Join(keys, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	f, err := os.Open(golden)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	expected, lost := 0, 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		expected++
		if !conflicts[line] {
			lost++
			t.Errorf("la guarda dejó de ver el par %s", line)
		}
	}
	t.Logf("pares a un dígito: %d; la guarda ve %d (esperados %d, perdidos %d)", pairs, len(conflicts), expected, lost)
}

// Los ocho pares dirigidos que la ronda 4 perdió (ESTERNOCLAVICULARES frente a TORAX; SACRO COXIS frente a
// DORSOLUMBAR, COLUMNA TOTAL y DINÁMICA DE COLUMNA) y el hueco previo (SACROCOXIGEA frente a LUMBOSACRA). Y lo
// que no debe escalar: cada nombre frente a su propio código. La dorsolumbar vale DORSAL y LUMBAR (sus niveles y
// «RX COLUMNA DORSAL Y LUMBAR», que es 871030): frente a la dorsal o la lumbar solas no es contradicción, como
// antes.
func TestCupsNamesConflict_OneDigitSiblingsOfTheReview(t *testing.T) {
	catalog := catalogFromTSV(t, "testdata/cups_catalog_active.tsv")
	name := func(code string) string {
		i := slices.IndexFunc(catalog, func(c CupsCandidate) bool { return c.Code == code })
		if i < 0 {
			t.Fatalf("%s no está en el catálogo", code)
		}
		return catalog[i].Name
	}
	conflicts := []struct{ read, code string }{
		{"RADIOGRAFIA DE ARTICULACIONES ESTERNOCLAVICULARES", "871121"},
		{"RX ESTERNOCLAVICULAR", "871121"},
		{"RADIOGRAFIA DE TORAX", "871129"},
		{"RX ESTERNOCLAVICULARES", "871112"},
		{"RADIOGRAFIA DE SACRO COXIS", "871030"},
		{"RADIOGRAFIA DE SACRO COXIS", "871060"},
		{"RADIOGRAFIA DE SACRO COXIS", "871070"},
		{"RADIOGRAFIA DE COLUMNA DORSOLUMBAR", "871050"},
		{"RADIOGRAFÍA DE COLUMNA VERTEBRAL TOTAL", "871050"},
		{"RADIOGRAFÍA DINÁMICA DE COLUMNA VERTEBRAL", "871050"},
		{"RM COLUMNA SACROCOXIGEA", "883230"},
		{"RM COLUMNA LUMBOSACRA", "883234"},
		{"RX COXIS", "871040"},
		{"RX COLUMNA DORSOLUMBAR", "871010"},
		{"RX COLUMNA TORACOLUMBAR", "871050"},
		{"RX COLUMNA CERVICAL", "871030"},
		// El sacro coxis se escribe sin COLUMNA (871050): frente a una columna sin segmento es otra región, como
		// en 6452bfd (871050 está a un dígito de 871030, 871040 y 871060).
		{"RX COLUMNA", "871050"},
	}
	for _, c := range conflicts {
		if !CupsNamesConflict(c.read, name(c.code)) {
			t.Errorf("guarda: %q frente a %s «%s» debe ser contradicción", c.read, c.code, name(c.code))
		}
	}
	coherent := []struct{ read, code string }{
		{"RX SACRO COXIS", "871050"},
		{"RX COXIS", "871050"},
		{"RX SACRO", "871050"},
		{"RX COLUMNA SACRA", "871050"},
		{"RX SACROCOXIGEA", "871050"},
		{"RM COLUMNA SACROCOXIGEA", "883234"},
		{"RM COLUMNA SACROCOCCIGEA", "883234"},
		{"RX COLUMNA DORSOLUMBAR", "871030"},
		{"RX COLUMNA TORACOLUMBAR", "871030"},
		{"RX COLUMNA DORSOLUMBAR T12 L1", "871030"},
		{"RX COLUMNA DORSAL Y LUMBAR", "871030"},
		{"RX COLUMNA DORSAL", "871030"},
		{"RX COLUMNA DORSOLUMBAR", "871020"},
		{"RX ESTERNOCLAVICULARES", "871129"},
		{"RX ARTICULACION ESTERNOCLAVICULAR", "871129"},
		{"RX TORAX", "871121"},
		{"RX COLUMNA", "871060"},
		{"RX COLUMNA LUMBAR DINAMICA", "871070"},
	}
	for _, c := range coherent {
		if CupsNamesConflict(c.read, name(c.code)) {
			t.Errorf("guarda: %q frente a %s «%s» no contradice", c.read, c.code, name(c.code))
		}
	}
	// Por nombre, el sacro coxis y la sacrocoxígea se asignan a su fila, y la dorsolumbar (también escrita como sus
	// dos segmentos) a la suya; la dorsal sola no es la dorsolumbar. «RX ESTERNOCLAVICULARES» sigue en el agente:
	// el catálogo dice ARTICULACIONES.
	for read, want := range map[string]string{
		"RX SACRO COXIS": "871050", "RX SACROCOXIGEA": "871050", "RM COLUMNA SACROCOXIGEA SIMPLE": "883234",
		"RX COLUMNA DORSOLUMBAR": "871030", "RX COLUMNA TORACOLUMBAR": "871030", "RX COLUMNA DORSAL Y LUMBAR": "871030",
		"RX ARTICULACIONES ESTERNOCLAVICULARES": "871129",
	} {
		got := acceptedByAdversary(read, catalog, false)
		if fmt.Sprint(got) != fmt.Sprint([]string{want}) {
			t.Errorf("%q → %v, quiero [%s]", read, got, want)
		}
	}
	for _, read := range []string{"RX COLUMNA DORSAL", "RM SACROCOXIGEA", "RX COLUMNA SACROCOXIGEA", "RX ESTERNOCLAVICULARES"} {
		if got := acceptedByAdversary(read, catalog, false); slices.Contains(got, "871030") || slices.Contains(got, "883234") ||
			slices.Contains(got, "871050") || slices.Contains(got, "871129") {
			t.Errorf("%q: se asignaría %v", read, got)
		}
	}
}
