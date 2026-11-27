// Package results es el cliente de la API de resultados de estudios.
package results

import (
	"sort"
	"strings"
	"time"
)

// Study es un estudio devuelto por la API de resultados.
type Study struct {
	PatientName string
	StudyType   string
	Admission   string
	File        string
	// Date es cero si fecha no se pudo leer; RawDate conserva el texto original.
	Date    time.Time
	RawDate string
}

// SortAndDedupe ordena por Date descendente (fechas cero al final) y quita los
// File repetidos, conservando el primero de cada uno tras ordenar.
func SortAndDedupe(studies []Study) []Study {
	sorted := make([]Study, len(studies))
	copy(sorted, studies)
	sort.SliceStable(sorted, func(i, j int) bool {
		zi, zj := sorted[i].Date.IsZero(), sorted[j].Date.IsZero()
		if zi != zj {
			return zj
		}
		return sorted[i].Date.After(sorted[j].Date)
	})
	seen := make(map[string]bool, len(sorted))
	out := make([]Study, 0, len(sorted))
	for _, s := range sorted {
		if seen[s.File] {
			continue
		}
		seen[s.File] = true
		out = append(out, s)
	}
	return out
}

var accentReplacer = strings.NewReplacer(
	"á", "a", "é", "e", "í", "i", "ó", "o", "ú", "u", "ü", "u", "ñ", "n",
)

func foldWords(s string) []string {
	return strings.Fields(accentReplacer.Replace(strings.ToLower(s)))
}

// NameMatches indica si todas las palabras completas del primer nombre y del
// primer apellido de SIESA están en el nombre de la API, sin tildes ni
// mayúsculas. Si alguna de las dos viene vacía devuelve false.
func NameMatches(firstName, firstSurname, apiName string) bool {
	first, surname := foldWords(firstName), foldWords(firstSurname)
	if len(first) == 0 || len(surname) == 0 {
		return false
	}
	have := make(map[string]bool)
	for _, w := range foldWords(apiName) {
		have[w] = true
	}
	for _, w := range append(first, surname...) {
		if !have[w] {
			return false
		}
	}
	return true
}
