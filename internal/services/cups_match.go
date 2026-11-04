package services

import "strings"

// PARECIDO ENTRE UN CÓDIGO/NOMBRE LEÍDO POR EL OCR Y EL CATÁLOGO (tomado de hdg-bot,
// internal/eligibility/cups_resolve.go). Lo usa la resolución de VALIDATE_OCR: un código que no se
// puede agendar tal cual se corrige solo con DOS señales a la vez —un nombre parecido en el catálogo
// y un código a un dígito del leído—, nunca con una sola.

// CupsNameSimilarityThreshold es la fracción de palabras distintivas del nombre leído que tienen que
// aparecer en el nombre del catálogo para considerarlos el mismo procedimiento.
const CupsNameSimilarityThreshold = 0.6

// genericProcedureWords están en casi todo nombre del catálogo y no distinguen un procedimiento de
// otro: no cuentan para el parecido. Sin esto, «CONSULTA DE PRIMERA VEZ POR ESPECIALISTA EN X» se
// parecería a TODAS las consultas de primera vez.
var genericProcedureWords = map[string]bool{
	"DE": true, "DEL": true, "LA": true, "EL": true, "LOS": true, "LAS": true, "POR": true,
	"PARA": true, "EN": true, "Y": true, "O": true, "A": true, "CON": true, "SIN": true, "AL": true,
	"UN": true, "UNA": true, "U": true, "E": true,
	"CONSULTA": true, "PRIMERA": true, "VEZ": true, "ESPECIALISTA": true, "MEDICINA": true,
	"CONTROL": true, "SEGUIMIENTO": true, "PROCEDIMIENTO": true, "EXAMEN": true, "ESTUDIO": true,
	"GENERAL": true, "TOTAL": true, "SIMPLE": true, "NUEVA": true, "NUEVO": true, "SOD": true,
}

// accentFolder quita tildes y diéresis (el texto ya va en mayúsculas).
var accentFolder = strings.NewReplacer("Á", "A", "É", "E", "Í", "I", "Ó", "O", "Ú", "U", "Ü", "U", "Ñ", "N")

// CupsNameSimilarity mide qué fracción de las palabras DISTINTIVAS del nombre leído aparecen en el
// nombre del catálogo (igualdad, o prefijo común de 5+ letras: OTORRINO ~ OTORRINOLARINGOLOGIA). 0 si
// el nombre leído no tiene ninguna palabra distintiva.
func CupsNameSimilarity(read, catalogName string) float64 {
	rt := distinctiveWords(read)
	if len(rt) == 0 {
		return 0
	}
	ct := procedureWords(catalogName)
	matched := 0
	for _, r := range rt {
		for _, c := range ct {
			if wordsMatch(r, c) {
				matched++
				break
			}
		}
	}
	return float64(matched) / float64(len(rt))
}

func wordsMatch(a, b string) bool {
	if a == b {
		return true
	}
	if len(a) >= 5 && len(b) >= 5 {
		return strings.HasPrefix(a, b) || strings.HasPrefix(b, a)
	}
	return false
}

// procedureWords parte el texto en palabras en mayúsculas y sin tildes, de 3+ caracteres.
func procedureWords(text string) []string {
	folded := accentFolder.Replace(strings.ToUpper(text))
	var out []string
	for _, w := range strings.FieldsFunc(folded, func(r rune) bool {
		return (r < 'A' || r > 'Z') && (r < '0' || r > '9')
	}) {
		if len(w) >= 3 {
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
