package handlers

import (
	"fmt"
	"sort"
	"strings"
)

// EL RESUMEN DEL AGENTE, EN SUS DOS FORMAS A LA VEZ.
//
// `buildAgentSummary` dejó de concatenar cadenas: construye una LISTA ORDENADA DE GRUPOS, el
// texto de siempre es el render de esa lista y `facts` es su serialización. Los dos se escriben
// EN LA MISMA SENTENCIA, y eso es lo que hace imposible que se separen.
//
// # Por qué no hay un motor de render genérico
//
// El texto de hoy lo fijan byte a byte los dorados de testdata/. Cada grupo escribe SU texto con
// el MISMO fmt.Sprintf de antes del refactor, movido verbatim, y los campos van al lado: un
// motor genérico sería un sitio más donde los dorados se rompen en silencio.
//
// # La regla del campo vacío
//
// Un campo por cada línea que el texto ESCRIBE. Se emite con `valor: ""` cuando el render
// escribe la línea igual (`Paciente:` y `Documento:` de un paciente sin identificar) y se OMITE
// cuando el render la omite (`Nota:`, `Servicio:`). Así `facts` y el texto llevan exactamente la
// misma información y ninguna de las dos es la versión completa.
//
// La excepción declarada: la línea FUSIONADA `Procedimiento: <nombre> (<cups>)` se parte en dos
// campos, porque los valores ya están separados en la sesión y la fusión es solo del render.

// LAS ETIQUETAS JSON SON DEL CONTRATO CON NÉCTAR. `clave`, `rotulo`, `valor`, `titulo` y
// `campos`, con su omitempty (o sin él), son las del esquema de Néctar, que lleva
// additionalProperties:false: renombrar o añadir una clave rompe la validación. `titulo` va SIN
// omitempty: un grupo sin título viaja como null. (El esquema admite además `items` con
// omitempty; Neuro no lo usa y su ausencia no cambia el cable.)
//
// summaryField es un par del resumen estructurado; los identificadores de Go van en inglés.
type summaryField struct {
	Key string `json:"clave"`
	// Label es opcional en `facts`: un par sin rótulo sigue siendo legible por su clave.
	Label string `json:"rotulo,omitempty"`
	Value string `json:"valor"`
}

// summaryGroup es un bloque del resumen. `titulo` NULO es un valor LEGAL y significa «bloque sin
// título» (por eso es puntero y NO lleva omitempty: ausente, vacío y nulo son tres datos
// distintos). Se maneja SIEMPRE por puntero: lleva un strings.Builder dentro y copiarlo sería
// copiar un valor que el propio vet marca.
type summaryGroup struct {
	Key    string         `json:"clave"`
	Title  *string        `json:"titulo"`
	Fields []summaryField `json:"campos,omitempty"`

	// text es el render EXACTO de ESTE bloque, tal como el resumen lo escribía antes.
	text strings.Builder
}

// line añade el campo Y escribe su línea «Rotulo: valor». Es el camino normal.
func (g *summaryGroup) line(key, label, value string) {
	g.field(key, label, value)
	fmt.Fprintf(&g.text, "%s: %s\n", label, value)
}

// optional es line solo si hay valor: los datos del caso (summary_case.go) son opcionales, y un
// grupo que se queda sin líneas no se emite.
func (g *summaryGroup) optional(key, label, value string) {
	if value == "" {
		return
	}
	g.line(key, label, value)
}

// tail es line SIN el salto final. Existe por una sola razón y conviene que se vea: el resumen
// TERMINA en «Equipo: <x>» sin salto de línea, y los dorados lo fijan así.
func (g *summaryGroup) tail(key, label, value string) {
	g.field(key, label, value)
	fmt.Fprintf(&g.text, "%s: %s", label, value)
}

// field añade el campo SIN escribir línea: para las líneas que no son «Rotulo: valor» (la
// fusionada de Procedimiento). El texto lo escribe raw(), con el MISMO fmt.Sprintf.
func (g *summaryGroup) field(key, label, value string) {
	g.Fields = append(g.Fields, summaryField{Key: key, Label: label, Value: value})
}

// raw escribe texto y nada más. Es el sitio por donde entran verbatim los fmt.Sprintf de antes.
func (g *summaryGroup) raw(s string) { g.text.WriteString(s) }

// empty dice si el grupo no llegó a escribir nada. Un grupo así NO se emite: es el bloque que
// hoy no aporta líneas y deja el resumen intacto.
func (g *summaryGroup) empty() bool {
	return g.text.Len() == 0 && len(g.Fields) == 0
}

// summaryDoc es el resumen entero: los grupos, en su orden exacto.
type summaryDoc struct{ groups []*summaryGroup }

// untitled abre un grupo SIN título (`titulo: null`).
func (d *summaryDoc) untitled(key string) *summaryGroup {
	g := &summaryGroup{Key: key}
	d.groups = append(d.groups, g)
	return g
}

// summaryGroupOrder es el orden TOTAL de los grupos del resumen. render ordena por él, así que
// el sitio donde se escribe cada grupo no altera el texto; los tres dorados viejos ya lo cumplen.
var summaryGroupOrder = []string{
	"encabezado", "paciente", "nota", "entidad", "procedimiento", "orden", "grupo",
	"agenda", "cita", "busqueda", "intento", "estado",
}

// groupRank es la posición del grupo en summaryGroupOrder; un grupo desconocido va al final.
func groupRank(key string) int {
	for i, k := range summaryGroupOrder {
		if k == key {
			return i
		}
	}
	return len(summaryGroupOrder)
}

// render devuelve el texto del resumen y los grupos que de verdad se escribieron, en el orden de
// summaryGroupOrder. Los dos salen de la MISMA lista, que es lo que garantiza que digan lo mismo.
func (d *summaryDoc) render() (string, []*summaryGroup) {
	var b strings.Builder
	ordered := append([]*summaryGroup(nil), d.groups...)
	sort.SliceStable(ordered, func(i, j int) bool { return groupRank(ordered[i].Key) < groupRank(ordered[j].Key) })
	out := make([]*summaryGroup, 0, len(ordered))
	for _, g := range ordered {
		if g.empty() {
			continue
		}
		b.WriteString(g.text.String())
		out = append(out, g)
	}
	return b.String(), out
}
