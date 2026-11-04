package services

import "strings"

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
// SEGUIMIENTO, TOTAL, la región, la lateralidad, la vía y los marcadores.
var genericProcedureWords = map[string]bool{
	"DE": true, "DEL": true, "LA": true, "EL": true, "LOS": true, "LAS": true, "POR": true,
	"PARA": true, "EN": true, "Y": true, "O": true, "A": true, "CON": true, "AL": true,
	"UN": true, "UNA": true, "U": true, "E": true,
	"CONSULTA": true, "ESPECIALISTA": true, "MEDICINA": true, "PROCEDIMIENTO": true,
	"EXAMEN": true, "ESTUDIO": true, "GENERAL": true, "NUEVA": true, "NUEVO": true, "SOD": true,
}

// accentFolder quita tildes y diéresis (el texto ya va en mayúsculas).
var accentFolder = strings.NewReplacer("Á", "A", "É", "E", "Í", "I", "Ó", "O", "Ú", "U", "Ü", "U", "Ñ", "N")

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
// distintivas en alguno de los dos no hay con qué comparar: CupsNamesDiffer.
func CompareCupsNames(a, b string) CupsNameMatch {
	aw, bw := distinctiveWords(a), distinctiveWords(b)
	switch {
	case len(aw) == 0 || len(bw) == 0:
		return CupsNamesDiffer
	case allWordsIn(aw, bw, sameWord) && allWordsIn(bw, aw, sameWord):
		return CupsNamesEqual
	case allWordsIn(aw, bw, prefixWord) && allWordsIn(bw, aw, prefixWord):
		return CupsNamesByPrefix
	}
	return CupsNamesDiffer
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

// prefixWord: iguales, o una es prefijo de la otra con 5+ letras (RODILLA ~ RODILLAS).
func prefixWord(a, b string) bool {
	if a == b {
		return true
	}
	if len(a) >= 5 && len(b) >= 5 {
		return strings.HasPrefix(a, b) || strings.HasPrefix(b, a)
	}
	return false
}

// procedureWords parte el texto en palabras en mayúsculas y sin tildes: las de 3+ caracteres y todos
// los números, que también distinguen («Paq. 1 electromiografía» frente a «Paq. 2 electromiografías»).
func procedureWords(text string) []string {
	folded := accentFolder.Replace(strings.ToUpper(text))
	var out []string
	for _, w := range strings.FieldsFunc(folded, func(r rune) bool {
		return (r < 'A' || r > 'Z') && (r < '0' || r > '9')
	}) {
		if len(w) >= 3 || strings.Trim(w, "0123456789") == "" {
			out = append(out, w)
		}
	}
	return out
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
