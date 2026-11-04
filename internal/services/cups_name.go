package services

import (
	"regexp"
	"sort"
	"strings"
)

// ASIGNACIÓN DEL CÓDIGO CUPS POR NOMBRE cuando la orden no lo trae (decisión del dueño, 2026-11-03).
// Antes el prompt de visión elegía de la lista del catálogo «la coincidencia más fuerte» y casi siempre
// elegía algo, aunque fuera otro procedimiento. Ahora el OCR devuelve el nombre tal cual y el bot
// resuelve aquí, con reglas que se pueden medir:
//
//  1. Un único procedimiento del catálogo con el nombre IGUAL (CompareCupsNames sobre los nombres
//     canónicos: sin tildes, con las abreviaturas de las órdenes expandidas) → se asigna.
//  2. Si no, hasta maxNameCandidates candidatos por parecido amplio de palabras; un modelo de solo texto
//     elige uno, y se acepta solo con confianza alta, si está en la lista, si su nombre no contradice
//     las palabras distintivas del leído (candidateFits) y si es el ÚNICO de los parecidos que encaja:
//     cuando el nombre leído no distingue entre dos («POTENCIALES VISUALES EVOCADOS» frente a
//     monofocales y multifocales), la elección sería una adivinanza.
//  3. Cualquier otra cosa es duda para un agente.

// CupsCandidate es un procedimiento del catálogo que puede corresponder a un nombre leído.
type CupsCandidate struct {
	Code string
	Name string
}

// maxNameCandidates es cuántos candidatos ve el modelo como máximo.
const maxNameCandidates = 5

// minCandidateScore es la fracción de palabras distintivas del nombre leído que tienen que estar en el
// del catálogo para que sea candidato (búsqueda amplia: el filtro fino es el del paso 2).
const minCandidateScore = 0.5

// CupsNameVia dice cómo se asignó un código por nombre.
const (
	CupsNameViaEqual = "nombre_igual" // un único nombre igual en el catálogo
	CupsNameViaModel = "modelo"       // el modelo eligió de la lista corta
)

// orderPhrases se reemplazan antes de partir en palabras (frases de las órdenes con un solo sentido).
var orderPhrases = strings.NewReplacer(
	"SIN MEDIO DE CONTRASTE", " SIMPLE ", "SIN CONTRASTE", " SIMPLE ",
	"1A VEZ", " PRIMERA VEZ ", "1RA VEZ", " PRIMERA VEZ ", "1ª VEZ", " PRIMERA VEZ ", "1° VEZ", " PRIMERA VEZ ",
	"RAYOS X", " RADIOGRAFIA ",
)

// orderAbbreviations expande (o quita, con "") las palabras de las órdenes a la forma del catálogo.
// Se aplica igual al nombre leído y al del catálogo, así «RNM CEREBRAL» y «RESONANCIA NUCLEAR MAGNETICA
// DE CEREBRO» quedan iguales.
var orderAbbreviations = map[string]string{
	"RM": "RESONANCIA MAGNETICA", "RMN": "RESONANCIA MAGNETICA", "RNM": "RESONANCIA MAGNETICA",
	"IRM": "RESONANCIA MAGNETICA", "NUCLEAR": "",
	"TAC": "TOMOGRAFIA", "TC": "TOMOGRAFIA", "CT": "TOMOGRAFIA", "TACAR": "TOMOGRAFIA",
	"AXIAL": "", "COMPUTADA": "", "COMPUTARIZADA": "",
	"ECO": "ECOGRAFIA", "US": "ECOGRAFIA", "ULTRASONOGRAFIA": "ECOGRAFIA", "ULTRASONIDO": "ECOGRAFIA",
	"RX":  "RADIOGRAFIA",
	"EMG": "ELECTROMIOGRAFIA", "NC": "NEUROCONDUCCION", "VCN": "NEUROCONDUCCION",
	"NEUROCONDUCCIONES": "NEUROCONDUCCION", "ELECTROMIOGRAFIAS": "ELECTROMIOGRAFIA",
	"EEG": "ELECTROENCEFALOGRAMA", "ELECTROENCEFALOGRAFIA": "ELECTROENCEFALOGRAMA",
	"PETCT":     "PET TOMOGRAFIA",
	"FISIATRIA": "FISICA REHABILITACION", "NEUROPEDIATRIA": "NEUROLOGIA PEDIATRICA",
	"MMII": "MIEMBROS INFERIORES", "MMSS": "MIEMBROS SUPERIORES",
	"DER": "DERECHA", "DCHA": "DERECHA", "DERECHO": "DERECHA", "IZQ": "IZQUIERDA", "IZQUIERDO": "IZQUIERDA",
	"BILAT":    "BILATERAL",
	"CEREBRAL": "CEREBRO", "ENCEFALO": "CEREBRO", "CRANEAL": "CRANEO",
	"CONTRASTADA": "CONTRASTE", "CONTRASTADO": "CONTRASTE", "GADOLINIO": "CONTRASTE",
	"PEDIATRICO": "PEDIATRICA", "PEDIATRIA": "PEDIATRICA",
}

// retrievalStopWords no aportan a la búsqueda de candidatos (encabezados de remisiones e interconsultas).
var retrievalStopWords = map[string]bool{
	"INTERCONSULTA": true, "REMISION": true, "VALORACION": true, "SERVICIO": true, "ESPECIALIDAD": true,
	"SOLICITUD": true, "ORDEN": true, "REFERENTE": true,
}

// quantityMark es la cantidad de las órdenes («(#4)», «#16»): no es parte del nombre.
var quantityMark = regexp.MustCompile(`#\s*\d+`)

// canonicalOrderName deja un nombre de procedimiento en la forma común de las órdenes y el catálogo:
// mayúsculas, sin tildes, con las frases y abreviaturas de orderPhrases y orderAbbreviations.
func canonicalOrderName(text string) string {
	folded := orderPhrases.Replace(accentFolder.Replace(strings.ToUpper(text)))
	folded = quantityMark.ReplaceAllString(folded, " ")
	var out []string
	for _, w := range strings.FieldsFunc(folded, func(r rune) bool {
		return (r < 'A' || r > 'Z') && (r < '0' || r > '9')
	}) {
		if exp, ok := orderAbbreviations[w]; ok {
			if exp != "" {
				out = append(out, exp)
			}
			continue
		}
		out = append(out, w)
	}
	return strings.Join(out, " ")
}

// CupsNameKey es la forma canónica de un nombre de procedimiento (sin tildes, mayúsculas y abreviaturas
// expandidas): sirve para reconocer el mismo procedimiento sin código escrito de dos maneras.
func CupsNameKey(name string) string { return canonicalOrderName(name) }

// firstVisitSuffix se suma al nombre leído de una interconsulta: es una consulta de primera vez.
const firstVisitSuffix = " CONSULTA PRIMERA VEZ"

// CupsNameResolution es el desenlace de ResolveCupsByName. Code vacío = duda para un agente.
type CupsNameResolution struct {
	Code       string
	Name       string // el nombre del catálogo
	Via        string
	Candidates []CupsCandidate // la lista corta (lo que ve el agente en una duda)
}

// ResolveCupsByName asigna el código de un procedimiento leído sin código. catalog son los candidatos
// posibles (para una interconsulta, solo las consultas de primera vez). choose es el modelo: recibe el
// nombre leído y la lista corta y devuelve el código elegido SOLO con confianza alta ("" si no, o si
// falló); nil = sin modelo.
func ResolveCupsByName(
	read string, catalog []CupsCandidate, interconsulta bool,
	choose func(read string, candidates []CupsCandidate) string,
) CupsNameResolution {
	// La búsqueda de candidatos usa solo lo leído; el sufijo de primera vez de una interconsulta cuenta
	// para el nombre igual y para encajar, no para encontrar candidatos de otra especialidad.
	retrievalRead := canonicalOrderName(read)
	canonRead := retrievalRead
	if interconsulta {
		canonRead += firstVisitSuffix
	}
	var equal []CupsCandidate
	for _, c := range catalog {
		if CompareCupsNames(canonRead, canonicalOrderName(c.Name)) == CupsNamesEqual {
			equal = append(equal, c)
		}
	}
	if len(equal) == 1 {
		return CupsNameResolution{Code: equal[0].Code, Name: equal[0].Name, Via: CupsNameViaEqual, Candidates: equal}
	}
	similar := nameCandidates(retrievalRead, catalog)
	res := CupsNameResolution{Candidates: similar[:min(len(similar), maxNameCandidates)]}
	if len(res.Candidates) == 0 || choose == nil {
		return res
	}
	var fitting []CupsCandidate
	for _, c := range similar {
		if candidateFits(canonRead, canonicalOrderName(c.Name)) {
			fitting = append(fitting, c)
		}
	}
	// Sin un único candidato que encaje no hay nada que confirmar: no se gasta la llamada.
	if len(fitting) != 1 || !containsCode(res.Candidates, fitting[0].Code) {
		return res
	}
	if code := strings.TrimSpace(choose(read, res.Candidates)); code == fitting[0].Code {
		res.Code, res.Name, res.Via = fitting[0].Code, fitting[0].Name, CupsNameViaModel
	}
	return res
}

func containsCode(list []CupsCandidate, code string) bool {
	for _, c := range list {
		if c.Code == code {
			return true
		}
	}
	return false
}

// nameCandidates son los del catálogo con al menos minCandidateScore de las palabras distintivas del
// leído, de mayor a menor parecido (y, a igual parecido, el de menos palabras de más). Los primeros
// maxNameCandidates son la lista corta que ve el modelo.
func nameCandidates(canonRead string, catalog []CupsCandidate) []CupsCandidate {
	var readWords []string
	for _, w := range distinctiveWords(canonRead) {
		if !retrievalStopWords[w] {
			readWords = append(readWords, w)
		}
	}
	if len(readWords) == 0 {
		return nil
	}
	type scored struct {
		c                 CupsCandidate
		recall, precision float64
	}
	var found []scored
	for _, c := range catalog {
		catWords := distinctiveWords(canonicalOrderName(c.Name))
		matched := 0
		for _, r := range readWords {
			for _, w := range catWords {
				if prefixWord(r, w) {
					matched++
					break
				}
			}
		}
		recall := float64(matched) / float64(len(readWords))
		if matched == 0 || recall < minCandidateScore {
			continue
		}
		found = append(found, scored{c, recall, float64(matched) / float64(max(len(catWords), 1))})
	}
	sort.SliceStable(found, func(i, j int) bool {
		if found[i].recall != found[j].recall {
			return found[i].recall > found[j].recall
		}
		return found[i].precision > found[j].precision
	})
	out := make([]CupsCandidate, 0, len(found))
	for _, f := range found {
		out = append(out, f.c)
	}
	return out
}

// CONTRADICCIÓN ENTRE NOMBRES. Las palabras que cambian el procedimiento se agrupan; dentro de un grupo
// los valores se excluyen (SIMPLE frente a CONTRASTE, PRIMERA VEZ frente a CONTROL, DERECHA frente a
// IZQUIERDA, una región frente a otra, 18-FLUOR frente a PSMA…).

type qualifierGroup struct {
	// values: palabra canónica → valor del grupo.
	values map[string]string
	// prefix: además de la palabra exacta, vale un prefijo común de 5+ letras (regiones: PELVIS ~
	// PELVICA, HOMBRO ~ HOMBROS).
	prefix bool
	// silentAs: en la comparación estricta, el nombre que no dice nada vale este valor (contraste: un
	// nombre sin la palabra es SIMPLE).
	silentAs string
	// readMayAdd: en la comparación estricta, el leído puede precisar el grupo sin que el candidato lo
	// diga (lateralidad: el catálogo tiene un solo código para los dos lados).
	readMayAdd bool
	// readSilentOK: en la comparación estricta, si el leído no dice nada del grupo no se compara (la
	// modalidad: «SILLA TURCA» a secas puede ser RX o TAC, y eso lo decide que encaje un solo candidato).
	readSilentOK bool
	// numbers: el grupo son los números del nombre (cantidad de un paquete: «Paq. 1» frente a «Paq. 2»).
	// Solo cuenta cuando los dos nombres traen números: el catálogo arrastra números sueltos («(62)»).
	numbers bool
}

func words(values ...string) map[string]string {
	m := make(map[string]string, len(values))
	for _, v := range values {
		k, val, ok := strings.Cut(v, "=")
		if !ok {
			val = k
		}
		m[k] = val
	}
	return m
}

var qualifierGroups = []qualifierGroup{
	{numbers: true},
	{values: words("RESONANCIA=RM", "TOMOGRAFIA=TC", "ECOGRAFIA=ECO", "RADIOGRAFIA=RX", "PET",
		"ELECTROMIOGRAFIA=EMG", "NEUROCONDUCCION=NC", "ELECTROENCEFALOGRAMA=EEG", "POLISOMNOGRAFIA"), readSilentOK: true},
	{values: words("DOPPLER")},
	{values: words("SIMPLE", "CONTRASTE"), silentAs: "SIMPLE"},
	{values: words("PRIMERA=PRIMERA_VEZ", "CONTROL", "SEGUIMIENTO=CONTROL")},
	{values: words("PEDIATRICA")},
	{values: words("DERECHA", "IZQUIERDA", "BILATERAL"), readMayAdd: true},
	{values: words("SUPERIOR", "SUPERIORES=SUPERIOR", "INFERIOR", "INFERIORES=INFERIOR")},
	{values: words("TRANSRECTAL", "TRANSABDOMINAL", "TRANSVAGINAL", "TRANSFONTANELAR", "TRANSCRANEAL")},
	{values: words("FLUOR=FDG", "FDG", "FLUORODESOXIGLUCOSA=FDG", "PSMA", "COLINA", "DOTATATE")},
	{values: words("FUNCIONAL", "TRACTOGRAFIA", "ESPECTROSCOPIA", "PERFUSION")},
	{values: words("CERVICAL", "DORSAL", "LUMBAR", "LUMBOSACRA", "SACRA", "SACRO=SACRA", "COCCIX")},
	{prefix: true, values: words(
		"CRANEO", "CEREBRO", "CARA", "ORBITA", "SENOS", "OIDO", "MASTOIDES", "HIPOFISIS", "SILLA",
		"CUELLO", "COLUMNA", "TORAX", "ABDOMEN", "PELVIS", "HOMBRO", "BRAZO", "HUMERO", "CODO",
		"ANTEBRAZO", "MUNECA", "MANO", "DEDOS", "CADERA", "MUSLO", "FEMUR", "RODILLA", "PIERNA", "TIBIA",
		"TOBILLO", "PIE", "CALCANEO", "MAXILAR", "MANDIBULA", "PROSTATA", "TIROIDES", "MAMA", "RENAL",
		"RINON", "HIGADO", "VESICULA", "ESCROTO", "TESTICULO", "SACROILIACA", "CLAVICULA", "ESTERNON",
		"COSTILLAS", "PARRILLA", "TEMPOROMANDIBULAR", "PIES=PIE",
	)},
}

// groupValues son los valores del grupo que menciona un nombre canónico.
func (g qualifierGroup) groupValues(canon string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.Fields(canon) {
		if g.numbers {
			if strings.Trim(w, "0123456789") == "" {
				out[w] = true
			}
			continue
		}
		if v, ok := g.values[w]; ok {
			out[v] = true
			continue
		}
		if !g.prefix || len(w) < 5 {
			continue
		}
		for k, v := range g.values {
			if len(k) >= 5 && w[:5] == k[:5] {
				out[v] = true
			}
		}
	}
	// SIMPLE Y CON CONTRASTE es un estudio contrastado, y el PET-TC es un PET.
	if out["CONTRASTE"] && out["SIMPLE"] {
		delete(out, "SIMPLE")
	}
	if out["PET"] {
		delete(out, "TC")
	}
	return out
}

func sameSet(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

func disjoint(a, b map[string]bool) bool {
	for k := range a {
		if b[k] {
			return false
		}
	}
	return true
}

// CupsNamesConflict dice si dos nombres se contradicen EXPLÍCITAMENTE: los dos mencionan un mismo grupo
// (contraste, tipo de consulta, lado, superior/inferior, vía, marcador del PET, columna, región) con
// valores distintos. Un nombre que no dice nada de un grupo no lo contradice: las órdenes abrevian.
func CupsNamesConflict(read, catalog string) bool {
	r, c := canonicalOrderName(read), canonicalOrderName(catalog)
	for _, g := range qualifierGroups {
		if g.numbers {
			continue // la cantidad de la orden («(#4)») no contradice al catálogo
		}
		rv, cv := g.groupValues(r), g.groupValues(c)
		if len(rv) > 0 && len(cv) > 0 && disjoint(rv, cv) {
			return true
		}
	}
	return false
}

// candidateFits es la comparación estricta para aceptar un candidato elegido por el modelo (nombres ya
// canónicos): en cada grupo los dos nombres dicen lo mismo, salvo que el leído puede precisar el lado
// y que, en el contraste, no decir nada vale SIMPLE. Un candidato que añade PEDIÁTRICA, FUNCIONAL, una
// región o un marcador que el leído no dice, no encaja.
func candidateFits(canonRead, canonCand string) bool {
	for _, g := range qualifierGroups {
		rv, cv := g.groupValues(canonRead), g.groupValues(canonCand)
		if g.silentAs != "" {
			if len(rv) == 0 {
				rv = map[string]bool{g.silentAs: true}
			}
			if len(cv) == 0 {
				cv = map[string]bool{g.silentAs: true}
			}
		}
		if g.readMayAdd && len(cv) == 0 || g.readSilentOK && len(rv) == 0 {
			continue
		}
		if g.numbers && (len(rv) == 0 || len(cv) == 0) {
			continue
		}
		if !sameSet(rv, cv) {
			return false
		}
	}
	return true
}
