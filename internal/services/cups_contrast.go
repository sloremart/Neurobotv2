package services

import (
	"regexp"
	"slices"
	"strings"
)

// LECTURA DEL CONTRASTE (revisión del lote C, ronda 4, R4-I2). Un falso «igual» entre un estudio simple y
// uno contrastado termina en una cita con el procedimiento equivocado, y tres rondas de frases sueltas no lo
// cerraron. Ahora hay UN clasificador, cerrado a fallos, que usan la asignación por nombre, el descarte entre
// hojas y la guarda de los CUPS con código:
//
//   - El texto llega normalizado (foldProcedureText): mayúsculas sin tildes, un solo espacio, la barra pegada
//     a sus letras («C / S» → «C/S»), las confusiones del OCR deshechas («N0» → «NO», «C0N» → «CON»).
//   - La frase del contraste es el tramo seguido de palabras del contraste (contrastWords) que trae alguna
//     mención: CONTRAST*, GADOLINIO, CTE, SIMPLE, una barra con C o S («C/S», «C/X»), «MEDIO» con CON o SIN, o un
//     CON o SIN al final del nombre. La frase ENTERA tiene que estar en la gramática (contrastGrammar), que
//     es la tabla de TestReadContrast_Grammar: si no encaja con exactitud, o hay dos frases que dicen
//     cosas distintas, o queda una negación suelta (NO, NI, NEGATIVO) junto a una mención, es DESCONOCIDO.
//   - La frase se reemplaza en el nombre por su valor (contrastSimple, contrastWith, contrastBoth,
//     contrastUnknown), que es una palabra del grupo del contraste. DESCONOCIDO no está en ningún nombre
//     del catálogo: nunca se asigna por nombre igual ni por el modelo, y va al agente.
//
// AMBOS («con y sin», 879113) solo es igual a una fila «SIMPLE Y CON CONTRASTE»: donde no la hay, es duda,
// no el simple ni el contrastado. En la guarda, frente al código con contraste no es contradicción (ese código
// es el que se agenda cuando no existe el de los dos); frente al simple, sí.

// contrastReading es lo que el nombre dice del contraste: la palabra que queda en el nombre canónico.
type contrastReading string

const (
	contrastSilent  contrastReading = ""                 // no dice nada
	contrastSimple  contrastReading = "SIMPLE"           // sin contraste
	contrastWith    contrastReading = "CONTRASTE"        // con contraste
	contrastBoth    contrastReading = "SIMPLEYCONTRASTE" // simple y contrastado («con y sin»)
	contrastUnknown contrastReading = "CONTRASTEDUDOSO"  // no se entiende: duda para un agente
)

// contrastValues son las palabras del grupo del contraste.
var contrastValues = words(string(contrastSimple), string(contrastWith), string(contrastBoth), string(contrastUnknown))

// contrastWords son las palabras que forman la frase del contraste con la mención (conectores incluidos).
var contrastWords = map[string]bool{
	"CON": true, "SIN": true, "Y": true, "O": true, "NO": true, "NI": true, "SI": true, "DE": true, "MEDIO": true,
	"SIMPLE": true, "CTE": true, "GADOLINIO": true, "?": true,
}

// contrastEdgeWords son conectores que, en el borde de la frase y pegados a otra palabra del nombre, son de
// esa otra palabra («SIMPLE Y CUELLO», «CON CONTRASTE DE CRANEO»). Al final del nombre se quedan: la frase
// está cortada («SIMPLE Y»).
var contrastEdgeWords = map[string]bool{"Y": true, "DE": true}

// negationWords sueltas junto a una mención dejan el contraste sin leer («NO REQUIERE CONTRASTE»).
var negationWords = map[string]bool{"NO": true, "NI": true, "NEGATIVO": true, "SIN": true}

// contrastNear son las palabras a las que una mención con una letra de más, de menos o cambiada se parece.
var contrastNear = []string{"CONTRASTE", "CONTRASTADO", "CONTRASTADA", "GADOLINIO"}

// contrastSlashForms son las abreviaturas con barra que la gramática entiende, puestas en palabras.
var contrastSlashForms = []struct {
	re   *regexp.Regexp
	repl string
}{
	{regexp.MustCompile(`\bC/S\b`), "CON/SIN"},
	{regexp.MustCompile(`\bS/C\b`), "SIN CONTRASTE"},
	{regexp.MustCompile(`\bC/C\b`), "CON CONTRASTE"},
	{regexp.MustCompile(`\bC/(CTE|CONTRAST[A-Z]*|MEDIO|GADOLINIO)\b`), "CON $1"},
	{regexp.MustCompile(`\bS/(CTE|CONTRAST[A-Z]*|MEDIO|GADOLINIO)\b`), "SIN $1"},
}

// contrastObject es el contraste con sus variantes: «MEDIO DE CONTRASTE», «CONTRASTADO», «GADOLINIO», «CTE».
const contrastObject = `(?:MEDIO (?:DE )?)?(?:CONTRASTE|CONTRASTADO|CONTRASTADA|GADOLINIO|CTE)`

// contrastGrammar son las frases que se entienden (la frase entera, ya sin barras). onlyAtEnd: la respuesta
// «CONTRASTE: NO» solo vale al final del nombre («CONTRASTE NO IONICO» lleva contraste).
var contrastGrammar = []struct {
	re        *regexp.Regexp
	reading   contrastReading
	onlyAtEnd bool
}{
	{regexp.MustCompile(`^SIMPLE$`), contrastSimple, false},
	{regexp.MustCompile(`^SIN ` + contrastObject + `$`), contrastSimple, false},
	{regexp.MustCompile(`^NO (?:CON )?` + contrastObject + `$`), contrastSimple, false},
	{regexp.MustCompile(`^(?:CON )?` + contrastObject + ` NO$`), contrastSimple, true},
	{regexp.MustCompile(`^(?:CON )?` + contrastObject + `$`), contrastWith, false},
	{regexp.MustCompile(`^(?:CON )?` + contrastObject + ` SI$`), contrastWith, true},
	{regexp.MustCompile(`^CON (?:Y|O) SIN ` + contrastObject + `$`), contrastBoth, false},
	{regexp.MustCompile(`^SIN (?:Y|O) CON ` + contrastObject + `$`), contrastBoth, false},
	{regexp.MustCompile(`^CON/SIN ` + contrastObject + `$`), contrastBoth, false},
	{regexp.MustCompile(`^SIN/CON ` + contrastObject + `$`), contrastBoth, false},
	{regexp.MustCompile(`^CON ` + contrastObject + ` (?:Y|O) SIN ` + contrastObject + `$`), contrastBoth, false},
	{regexp.MustCompile(`^SIN ` + contrastObject + ` (?:Y|O) CON ` + contrastObject + `$`), contrastBoth, false},
	{regexp.MustCompile(`^SIMPLE Y (?:CON )?` + contrastObject + `$`), contrastBoth, false},
	// Los valores ya leídos (la forma canónica es estable).
	{regexp.MustCompile(`^` + string(contrastBoth) + `$`), contrastBoth, false},
	{regexp.MustCompile(`^` + string(contrastUnknown) + `$`), contrastUnknown, false},
}

// readContrast lee el contraste de un nombre normalizado (foldProcedureText) y devuelve la lectura y el nombre
// con cada frase del contraste reemplazada por su valor. Sin mención, el nombre vuelve tal cual.
func readContrast(folded string) (contrastReading, string) {
	var tokens []string
	for _, t := range strings.Fields(folded) {
		// Una barra que no es del contraste («EMG/NC», «TAC/CONTRASTE») separa palabras.
		if strings.Contains(t, "/") && !isContrastWord(t) {
			tokens = append(tokens, strings.Split(t, "/")...)
			continue
		}
		tokens = append(tokens, t)
	}
	reading := contrastSilent
	var out []string
	mentioned := false
	for i := 0; i < len(tokens); {
		if !isContrastWord(tokens[i]) {
			out = append(out, tokens[i])
			i++
			continue
		}
		start := i
		for i < len(tokens) && isContrastWord(tokens[i]) {
			i++
		}
		run := tokens[start:i]
		if !mentionsContrast(run, i == len(tokens)) {
			out = append(out, run...)
			continue
		}
		mentioned = true
		// Los conectores del borde pegados a otra palabra del nombre se quedan fuera de la frase.
		var before, after []string
		for start > 0 && len(run) > 0 && contrastEdgeWords[run[0]] {
			before, run = append(before, run[0]), run[1:]
		}
		for i < len(tokens) && len(run) > 0 && contrastEdgeWords[run[len(run)-1]] {
			after, run = append([]string{run[len(run)-1]}, after...), run[:len(run)-1]
		}
		got := classifyContrastPhrase(strings.Join(run, " "), i == len(tokens))
		switch {
		case reading == contrastSilent:
			reading = got
		case reading != got:
			reading = contrastUnknown
		}
		out = append(append(append(out, before...), "\x00"), after...)
	}
	if mentioned && slices.ContainsFunc(out, func(w string) bool { return negationWords[w] }) {
		reading = contrastUnknown
	}
	for i, w := range out {
		if w == "\x00" {
			out[i] = string(reading)
		}
	}
	return reading, strings.Join(out, " ")
}

// classifyContrastPhrase busca la frase entera en la gramática.
func classifyContrastPhrase(phrase string, atEnd bool) contrastReading {
	for _, f := range contrastSlashForms {
		phrase = f.re.ReplaceAllString(phrase, f.repl)
	}
	for _, g := range contrastGrammar {
		if (!g.onlyAtEnd || atEnd) && g.re.MatchString(phrase) {
			return g.reading
		}
	}
	return contrastUnknown
}

// isContrastWord dice si la palabra puede formar parte de la frase del contraste. Con barra, todas sus
// partes tienen que serlo, o ser letras sueltas («CON/SIN», «Y/O», «C/X», «N/A», «C/CONTRASTE»); «EMG/NC» no.
func isContrastWord(w string) bool {
	if strings.Contains(w, "/") {
		for _, part := range strings.Split(w, "/") {
			if !contrastWords[part] && !isContrastMention(part) && len(part) != 1 {
				return false
			}
		}
		return true
	}
	return contrastWords[w] || isContrastMention(w)
}

// isContrastMention dice si la palabra nombra el contraste, también cortada («CONTRAS») o con una letra
// cambiada («CONTRATE»), o es una sigla con barra con C o S («C/S», «C/X»), o una de las lecturas.
func isContrastMention(w string) bool {
	if strings.HasPrefix(w, "CONTRAS") || strings.Contains(w, "CONTRAST") || w == "GADOLINIO" || w == "CTE" ||
		w == "SIMPLE" || w == "CC" || w == "SC" || contrastValues[w] != "" {
		return true
	}
	if parts := strings.Split(w, "/"); len(parts) > 1 {
		return slices.ContainsFunc(parts, func(p string) bool { return p == "C" || p == "S" || isContrastMention(p) })
	}
	return len(w) >= 8 && slices.ContainsFunc(contrastNear, func(near string) bool { return oneEditApart(w, near) })
}

// mentionsContrast dice si el tramo habla del contraste: trae una mención, o «MEDIO» con CON o SIN, o CON y
// SIN juntos («con y sin»), o acaba el nombre en CON o SIN (cortado).
func mentionsContrast(run []string, atEnd bool) bool {
	hasCon, hasSin, hasMedio := false, false, false
	for _, w := range run {
		if isContrastMention(w) {
			return true
		}
		hasCon, hasSin, hasMedio = hasCon || w == "CON", hasSin || w == "SIN", hasMedio || w == "MEDIO"
	}
	return hasCon && hasSin || hasMedio && (hasCon || hasSin) || atEnd && (hasCon || hasSin)
}

// oneEditApart dice si dos palabras se diferencian en una letra de más, de menos o cambiada.
func oneEditApart(a, b string) bool {
	if a == b {
		return true
	}
	if len(a) < len(b) {
		a, b = b, a
	}
	if len(a)-len(b) > 1 {
		return false
	}
	i := 0
	for i < len(b) && a[i] == b[i] {
		i++
	}
	if len(a) == len(b) {
		return a[i+1:] == b[i+1:]
	}
	return a[i+1:] == b[i:]
}

// contrastOf es la lectura del contraste que ya lleva un nombre canónico.
func contrastOf(canon string) contrastReading {
	for _, w := range strings.Fields(canon) {
		if v, ok := contrastValues[w]; ok {
			return contrastReading(v)
		}
	}
	return contrastSilent
}

// ContrastUnclear dice si el nombre menciona el contraste de una forma que no se entiende: VALIDATE_OCR lo
// manda al agente con esa nota, con código o sin él.
func ContrastUnclear(name string) bool {
	return contrastOf(canonicalOrderName(name)) == contrastUnknown
}

// contrastCompatible dice si dos lecturas del contraste no se contradicen en la guarda: «con y sin» frente al
// código con contraste no lo es (ese código se agenda cuando no existe el de los dos); frente al simple, sí.
func contrastCompatible(a, b map[string]bool) bool {
	both, with := string(contrastBoth), string(contrastWith)
	return a[both] && b[with] || a[with] && b[both]
}
