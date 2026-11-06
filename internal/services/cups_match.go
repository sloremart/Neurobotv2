package services

import (
	"regexp"
	"slices"
	"strings"
)

// PARECIDO ENTRE UN CÓDIGO/NOMBRE LEÍDO POR EL OCR Y EL CATÁLOGO (tomado de hdg-bot,
// internal/eligibility/cups_resolve.go, y endurecido tras la revisión de 2026-11-03). Lo usa la
// resolución de VALIDATE_OCR: un código que no se puede agendar tal cual se corrige solo con DOS señales
// a la vez —el mismo procedimiento por nombre en el catálogo y un código a un dígito del leído—.
//
// En el catálogo de Neuro los hermanos (superior/inferior, simple/contraste, primera vez/control,
// transrectal/transabdominal, PET 18-FLUOR/PSMA) suelen estar justo a un dígito. Por eso el nombre no
// puede ser «parecido» por fracción de palabras, como en hdg (allí los candidatos son una ficha
// contratada pequeña; aquí, el catálogo entero): tiene que describir el MISMO procedimiento.

// genericProcedureWords no distinguen un procedimiento de otro: no cuentan al comparar nombres. NO van
// aquí las que sí cambian el procedimiento: SIMPLE, SIN y CON CONTRASTE, PRIMERA VEZ y CONTROL o
// SEGUIMIENTO, TOTAL, la región, la lateralidad, la vía y los marcadores. Es una lista cerrada también para
// lo corto: cualquier otra palabra, por corta que sea (NO, D, OD, L4, 3D, 6H), se compara. AP y PA son la
// proyección, y el catálogo reúne las dos en un código (871121 «P.A. Ó A.P.»).
var genericProcedureWords = map[string]bool{
	"DE": true, "DEL": true, "LA": true, "EL": true, "LOS": true, "LAS": true, "POR": true,
	"PARA": true, "EN": true, "Y": true, "O": true, "A": true, "CON": true, "AL": true,
	"UN": true, "UNA": true, "U": true, "E": true, "AP": true, "PA": true,
	"CONSULTA": true, "ESPECIALISTA": true, "MEDICINA": true, "GENERAL": true, "NUEVA": true, "NUEVO": true, "SOD": true,
}

// vagueProcedureWords no dicen qué procedimiento es, pero tampoco son una consulta: «EXAMEN NEUROLOGIA» no es
// «CONSULTA NEUROLOGIA», son asuntos distintos. Por eso sí se comparan, aunque solas no bastan para comparar
// (dropVagueWords las quita cuando el nombre ya escribe la modalidad).
var vagueProcedureWords = map[string]bool{"PROCEDIMIENTO": true, "EXAMEN": true, "ESTUDIO": true}

// hasSpecificWord dice si hay alguna palabra con la que comparar.
func hasSpecificWord(words []string) bool {
	return slices.ContainsFunc(words, func(w string) bool { return !vagueProcedureWords[w] })
}

// accentFolder quita tildes y diéresis (el texto ya va en mayúsculas).
var accentFolder = strings.NewReplacer("Á", "A", "É", "E", "Í", "I", "Ó", "O", "Ú", "U", "Ü", "U", "Ñ", "N")

// dottedLetters son letras sueltas con punto que forman una sigla («P.A.», «M.D.», «O.D.»): sin unirlas
// quedarían letras sueltas.
var dottedLetters = regexp.MustCompile(`\b([A-Z])\.\s?([A-Z])\b\.?`)

// ocrConfusions deshace las confusiones del OCR que cambian el sentido: la O leída como cero («N0», «C0N»,
// «C0NTRASTE») y la S como cinco («5IN»). Van sobre el texto con un solo espacio entre palabras.
var ocrConfusions = []struct {
	re   *regexp.Regexp
	repl string
}{
	{regexp.MustCompile(`\bN ?0\b`), "NO"},
	{regexp.MustCompile(`\bC0N\b`), "CON"},
	{regexp.MustCompile(`C0NTRAST`), "CONTRAST"},
	{regexp.MustCompile(`\b5IN\b`), "SIN"},
}

// quantityMark es la cantidad escrita en el nombre («(#4)», «X2», «N 2», «No. 1», «NO2»): no es parte del
// nombre. «NO» solo vale como marca con un signo o pegado a la cifra: «NO 1 CONTRASTE» no es una cantidad.
var quantityMark = regexp.MustCompile(`#\s*\d+|\bX\s*\d+\b|\b(?:N|NRO|NUM)\s*[.:°º]*\s*\d+\b|\bNO\s*[.:°º]+\s*\d+\b|\bNO\d+\b`)

// nonNameChars es todo lo que no es letra, cifra, barra ni interrogación: la barra la lee el contraste
// («C/S»), y la interrogación («CONTRASTE?») lo deja en duda.
var nonNameChars = regexp.MustCompile(`[^A-Z0-9/?]+`)

// contrastSigns son los signos y casillas con que una orden niega, afirma o deja vacío un campo («CONTRASTE (-)»,
// «Ø CONTRASTE», «CONTRASTE: 0», «CON CONTRASTE ( )», «CONTRASTE ✗»): una casilla vacía o con solo un signo, un
// símbolo de negación o de marca, un guion o una raya que no une dos palabras, y un cero suelto. Se vuelven la
// ficha «?» ANTES de quitar los signos, para que el contraste los vea (R5-C1): pegados a su mención dejan la
// frase fuera de la gramática (DESCONOCIDO); lejos de ella se pierden como cualquier signo. El guion que une
// dos palabras («L4-L5», «SIN-CONTRASTE», «PET-TC») sigue siendo un separador.
var contrastSigns = []*regexp.Regexp{
	regexp.MustCompile(`[(\[] *[-–—+_0]* *[)\]]`),
	regexp.MustCompile(`[Ø∅✗✘✓✔☐☑☒×]`),
	regexp.MustCompile(`(^|[^A-Z0-9])[-–—_]+($|[^A-Z0-9])`),
	regexp.MustCompile(`(^|[^A-Z0-9])0($|[^A-Z0-9])`),
}

// slashSpaces pega la barra a sus letras: «C / S» es «C/S».
var slashSpaces = regexp.MustCompile(` */ *`)

// spineLevel es un nivel de columna o varios pegados («L4», «T12», «L4L5»).
var spineLevel = regexp.MustCompile(`^(?:[CTDLS]\d{1,2})+$`)

// prefoldProcedureText pasa a mayúsculas sin tildes, une las siglas con punto, deja un solo espacio entre
// palabras y deshace las confusiones del OCR: lo común a las palabras y a las marcas de cantidad.
func prefoldProcedureText(text string) string {
	folded := accentFolder.Replace(strings.ToUpper(text))
	folded = dottedLetters.ReplaceAllString(folded, " $1$2 ")
	folded = strings.Join(strings.Fields(folded), " ")
	for _, c := range ocrConfusions {
		folded = c.re.ReplaceAllString(folded, c.repl)
	}
	return folded
}

// foldProcedureText normaliza un nombre de procedimiento: prefoldProcedureText, sin las marcas de cantidad
// (compareReadNames las compara aparte), los signos y casillas como «?» (contrastSigns), «+» como Y, sin otros
// signos salvo la barra pegada a sus letras, y un solo espacio entre palabras. Es el texto que lee el contraste
// (readContrast) y que se parte en palabras.
func foldProcedureText(text string) string {
	folded := quantityMark.ReplaceAllString(prefoldProcedureText(text), " ")
	for _, sign := range contrastSigns {
		folded = sign.ReplaceAllString(folded, "$1 ? $2")
	}
	folded = strings.NewReplacer("+", " Y ", "?", " ? ").Replace(folded)
	folded = slashSpaces.ReplaceAllString(nonNameChars.ReplaceAllString(folded, " "), "/")
	return strings.Join(strings.Fields(folded), " ")
}

// CupsNameMatch es cuánto se parecen dos nombres de procedimiento (CompareCupsNames).
type CupsNameMatch int

const (
	// CupsNamesDiffer indica alguna palabra distintiva de más o de menos: otro procedimiento.
	CupsNamesDiffer CupsNameMatch = iota
	// CupsNamesByPrefix indica las mismas palabras, pero alguna solo por prefijo de 5+ letras (singular y
	// plural: MIEMBROS INFERIORES frente a MIEMBRO INFERIOR, que en CUPS es bilateral frente a
	// unilateral). No basta para corregir: es duda.
	CupsNamesByPrefix
	// CupsNamesEqual indica las mismas palabras distintivas, iguales tras quitar tildes y mayúsculas. Es el
	// mismo procedimiento.
	CupsNamesEqual
)

// CompareCupsNames compara dos nombres por sus palabras distintivas, en los DOS sentidos. Cualquier
// palabra distintiva de más o de menos —RODILLA frente a HOMBRO, SIMPLE frente a CONTRASTE, PRIMERA VEZ
// frente a CONTROL, PEDIÁTRICA, SUPERIOR, TRANSRECTAL, PSMA, FUNCIONAL— da CupsNamesDiffer. Sin palabras
// distintivas en alguno de los dos no hay con qué comparar: CupsNamesDiffer. Es la comparación de asignar un
// código: el nivel de columna que solo trae un nombre es silencio, porque el catálogo no lo distingue, salvo
// que contradiga la región escrita.
func CompareCupsNames(a, b string) CupsNameMatch {
	aw, bw := distinctiveWords(a), distinctiveWords(b)
	if hasSpineLevel(aw) != hasSpineLevel(bw) && !levelsContradictRegion(canonicalOrderName(a+" "+b)) {
		aw, bw = withoutSpineLevels(aw), withoutSpineLevels(bw)
	}
	switch {
	case !hasSpecificWord(aw) || !hasSpecificWord(bw):
		return CupsNamesDiffer
	case allWordsIn(aw, bw, sameWord) && allWordsIn(bw, aw, sameWord):
		return CupsNamesEqual
	case allWordsIn(aw, bw, prefixWord) && allWordsIn(bw, aw, prefixWord):
		return CupsNamesByPrefix
	}
	return CupsNamesDiffer
}

func hasSpineLevel(words []string) bool {
	return slices.ContainsFunc(words, spineLevel.MatchString)
}

func withoutSpineLevels(words []string) []string {
	return slices.DeleteFunc(slices.Clone(words), spineLevel.MatchString)
}

// singleSpineLevel separa los niveles pegados («L4L5»).
var singleSpineLevel = regexp.MustCompile(`[CTDLS]\d{1,2}`)

// isSpineRegion dice si la palabra canónica es un segmento de columna (spineGroup, cups_name.go: TORACICA y
// TORACICO son DORSAL, LUMBOSACRA es LUMBAR, DORSOLUMBAR es DORSAL LUMBAR, SACROCOXIGEA es SACRO COXIS).
func isSpineRegion(w string) bool { return spineGroup.values[w] != "" }

// levelFitsRegion dice si un nivel cabe en un segmento de columna (su valor en spineGroup). Los discos entre dos
// regiones (C7-T1, T12-L1, L5-S1) caben en las dos. LUMBAR también es la lumbosacra, que llega a S1 y no más
// abajo: S2 en adelante es la sacrocoxígea, así que es duda. La dorsolumbar escribe sus dos segmentos, y el nivel
// cabe en cualquiera.
func levelFitsRegion(level, region string) bool {
	switch region {
	case "CERVICAL":
		return level[0] == 'C' || level == "T1" || level == "D1"
	case "DORSAL":
		return level[0] == 'T' || level[0] == 'D' || level == "C7" || level == "L1"
	case "LUMBAR":
		return level[0] == 'L' || level == "S1" || level == "T12" || level == "D12"
	default: // SACRO, COXIS
		return level[0] == 'S' || level == "L5"
	}
}

// levelsContradictRegion dice si un nombre canónico trae un nivel que no cabe en ninguna región de columna
// escrita: «COLUMNA CERVICAL L4». Sin niveles o sin región no hay nada que contradecir. Para comparar dos
// nombres se le pasan juntos: «COLUMNA L4» contradice a un catálogo que dice cervical.
func levelsContradictRegion(canon string) bool {
	words := strings.Fields(canon)
	var regions []string
	for _, w := range words {
		if isSpineRegion(w) {
			regions = append(regions, spineGroup.values[w])
		}
	}
	if len(regions) == 0 {
		return false
	}
	for _, w := range words {
		if !spineLevel.MatchString(w) {
			continue
		}
		for _, level := range singleSpineLevel.FindAllString(w, -1) {
			fits := slices.ContainsFunc(regions, func(region string) bool { return levelFitsRegion(level, region) })
			if !fits {
				return true
			}
		}
	}
	return false
}

// allWordsIn dice si cada palabra de words tiene su par en other según match.
func allWordsIn(words, other []string, match func(a, b string) bool) bool {
	for _, w := range words {
		found := false
		for _, o := range other {
			if match(w, o) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func sameWord(a, b string) bool { return a == b }

// prefixWord: iguales, o una es prefijo de la otra con 5+ letras (RODILLA ~ RODILLAS, OTORRINO ~
// OTORRINOLARINGOLOGIA). Sirve para buscar candidatos y para sospechar (CupsNamesByPrefix), nunca para dar
// por igual: eso lo hace pluralWord.
func prefixWord(a, b string) bool {
	if a == b {
		return true
	}
	if len(a) >= 5 && len(b) >= 5 {
		return strings.HasPrefix(a, b) || strings.HasPrefix(b, a)
	}
	return false
}

// pluralWord: iguales, o una es el plural de la otra (RODILLA ~ RODILLAS, EXTREMIDAD ~ EXTREMIDADES). Es lo
// que vale para que un candidato ENCAJE o una región sea la misma: un prefijo cualquiera no (ESTERNON no es
// ESTERNOCLAVICULAR, NEURO no es NEUROLOGIA, CEREBRO no es CEREBELO; ronda 4).
func pluralWord(a, b string) bool {
	if a == b {
		return true
	}
	if len(a) < len(b) {
		a, b = b, a
	}
	return len(b) >= 4 && strings.HasPrefix(a, b) && (a[len(b):] == "S" || a[len(b):] == "ES")
}

// procedureWords parte el texto en palabras en mayúsculas y sin tildes. Todas, también las cortas y los
// números: distinguen el lado (D, OD), el nivel (L4), la técnica o la duración (3D, 6H), la negación (NO) y
// el paquete («Paq. 1 electromiografía» frente a «Paq. 2 electromiografías»).
func procedureWords(text string) []string {
	return strings.FieldsFunc(foldProcedureText(text), func(r rune) bool {
		return (r < 'A' || r > 'Z') && (r < '0' || r > '9')
	})
}

// distinctiveWords son las palabras del nombre que sí distinguen el procedimiento.
func distinctiveWords(text string) []string {
	var out []string
	for _, w := range procedureWords(text) {
		if !genericProcedureWords[w] {
			out = append(out, w)
		}
	}
	return out
}

// CupsOneDigitApart dice si dos códigos del mismo largo difieren en UN dígito (sustitución:
// 890279 ↔ 890274) o en dos adyacentes trocados (transposición: 890274 ↔ 890247).
func CupsOneDigitApart(a, b string) bool {
	if len(a) != len(b) || a == b || len(a) < 4 {
		return false
	}
	diff := -1
	for i := 0; i < len(a); i++ {
		if a[i] == b[i] {
			continue
		}
		if diff >= 0 {
			// Segunda diferencia: solo vale si es la transposición de la anterior.
			return diff == i-1 && a[diff] == b[i] && a[i] == b[diff] && a[i+1:] == b[i+1:]
		}
		diff = i
	}
	return diff >= 0
}
