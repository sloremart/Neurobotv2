package services

import (
	"strings"
	"sync"
	"sync/atomic"
)

// FORMAS CANÓNICAS DEL CATÁLOGO, CALCULADAS UNA VEZ (ronda 6c). ResolveCupsByName canonizaba el catálogo entero
// por cada lectura, tres o cuatro veces (filas con región, nombre igual, candidatos, encaje, anotación,
// diagnóstico), y cada canonización es una docena de expresiones regulares: con -race la suite de services
// pasaba de los 10 minutos. La clave es el nombre tal cual: un nombre que cambia en el catálogo es otra clave, así
// que no hay nada que invalidar cuando el catálogo se recarga; lo que no se puede es mutar lo cacheado (las
// listas son de solo lectura). El tope evita que crezca sin límite si alguien pasa lecturas como catálogo: por
// encima se calcula sin guardar.

// catalogRow son las formas canónicas de un nombre del catálogo.
type catalogRow struct {
	canon     string            // canonicalOrderName del nombre
	words     []string          // distinctiveWords(canon)
	groups    []map[string]bool // groupValues(canon) por cada qualifierGroups
	core      string            // canonicalOrderName del nombre sin anotaciones (lo entre paréntesis)
	coreWords []string          // distinctiveWords(core)
}

const maxCatalogRows = 4096

var (
	catalogRows     sync.Map // nombre → *catalogRow
	catalogRowCount atomic.Int32
)

// rowOf devuelve las formas canónicas de un nombre del catálogo, de la caché o calculándolas una vez.
func rowOf(name string) *catalogRow {
	if r, ok := catalogRows.Load(name); ok {
		return r.(*catalogRow)
	}
	r := newCatalogRow(name)
	if catalogRowCount.Load() < maxCatalogRows {
		if _, loaded := catalogRows.LoadOrStore(name, r); !loaded {
			catalogRowCount.Add(1)
		}
	}
	return r
}

// newCatalogRow calcula las formas canónicas de un nombre (sin guardarlas: para las lecturas).
func newCatalogRow(name string) *catalogRow {
	canon := canonicalOrderName(name)
	core := canonicalOrderName(catalogAnnotation.ReplaceAllString(name, " "))
	return &catalogRow{
		canon: canon, words: distinctiveWords(canon), groups: groupValuesOf(canon),
		core: core, coreWords: distinctiveWords(core),
	}
}

// groupValuesOf son los valores de cada grupo de calificadores de un nombre canónico, en el orden de qualifierGroups.
func groupValuesOf(canon string) []map[string]bool {
	out := make([]map[string]bool, len(qualifierGroups))
	for i, g := range qualifierGroups {
		out[i] = g.groupValues(canon)
	}
	return out
}

// readSide son las formas de la lectura canónica que el encaje (rowFits) compara con cada candidato: se calculan
// una vez por lectura, no por candidato.
type readSide struct {
	canon   string
	fields  []string
	words   []string          // distinctiveWords(canon)
	content []string          // contentWords(canon, orderNoise), sin EXTREMIDAD en EMG/NC/potenciales ni OBLICUAS en columna
	groups  []map[string]bool // groupValues(canon) por cada qualifierGroups
}

func newReadSide(canonRead string) *readSide {
	fields := strings.Fields(canonRead)
	words := distinctiveWords(canonRead)
	content := contentOf(words, orderNoise)
	if namesLimbStudy(fields) {
		content = deleteWords(content, func(w string) bool { return limbWords[w] })
	}
	// 873444 es solo de extremidades: en una columna, el catálogo no distingue las oblicuas. Si el catálogo añade
	// un código de oblicuas de columna, esta regla hay que retirarla.
	if containsWord(fields, "COLUMNA") {
		content = deleteWords(content, func(w string) bool { return w == "OBLICUA" || w == "OBLICUAS" })
	}
	return &readSide{canon: canonRead, fields: fields, words: words, content: content, groups: groupValuesOf(canonRead)}
}

// deleteWords devuelve una copia sin las palabras que cumplen del (las listas cacheadas no se tocan).
func deleteWords(words []string, del func(string) bool) []string {
	out := make([]string, 0, len(words))
	for _, w := range words {
		if !del(w) {
			out = append(out, w)
		}
	}
	return out
}

func containsWord(words []string, word string) bool {
	for _, w := range words {
		if w == word {
			return true
		}
	}
	return false
}
