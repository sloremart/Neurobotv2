package services

import (
	"regexp"
	"slices"
	"sort"
	"strings"
)

// ASIGNACIÓN DEL CÓDIGO CUPS POR NOMBRE cuando la orden no lo trae (decisión del dueño, 2026-11-03).
// Antes el prompt de visión elegía de la lista del catálogo «la coincidencia más fuerte» y casi siempre
// elegía algo, aunque fuera otro procedimiento. Ahora el OCR devuelve el nombre tal cual y el bot
// resuelve aquí, con reglas que se pueden medir:
//
//  1. Un único procedimiento del catálogo con el nombre IGUAL (CompareCupsNames sobre los nombres
//     canónicos: sin tildes, con las abreviaturas de las órdenes expandidas y el contraste leído por
//     readContrast, cups_contrast.go) → se asigna.
//  2. Si no, hasta maxNameCandidates candidatos por parecido amplio de palabras; un modelo de solo texto
//     elige uno, y se acepta solo con confianza alta, si está en la lista y si es el ÚNICO de los
//     parecidos que ENCAJA con el leído (candidateFits): las mismas palabras de contenido en los dos
//     sentidos —salvo palabras vacías y anotaciones del catálogo— y lo mismo en cada grupo de
//     calificadores. El modelo solo confirma o desempata lo que ya coincide; cuando el leído no distingue
//     entre dos candidatos, la elección sería una adivinanza.
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

// orderPhrases se reemplazan antes de partir en palabras (frases de las órdenes con un solo sentido). Van
// sobre el texto normalizado y ya sin la frase del contraste (readContrast): «1ª VEZ» y «1° VEZ» llegan como
// «1 VEZ».
var orderPhrases = strings.NewReplacer(
	"TOMOGRAFIA POR EMISION DE POSITRONES", " PET ",
	"VASOS ARTERIALES", " ARTERIAL ", "VASOS VENOSOS", " VENOSO ", "COXO FEMORAL", " CADERA ",
	"1A VEZ", " PRIMERA VEZ ", "1RA VEZ", " PRIMERA VEZ ", "1 VEZ", " PRIMERA VEZ ",
	"RAYOS X", " RADIOGRAFIA ",
)

// orderAbbreviations expande (o quita, con "") las palabras de las órdenes a la forma del catálogo.
// Se aplica igual al nombre leído y al del catálogo, así «RNM CEREBRAL» y «RESONANCIA NUCLEAR MAGNETICA
// DE CEREBRO» quedan iguales.
var orderAbbreviations = map[string]string{
	"RM": "RESONANCIA MAGNETICA", "RMN": "RESONANCIA MAGNETICA", "RNM": "RESONANCIA MAGNETICA",
	"IRM": "RESONANCIA MAGNETICA", "NUCLEAR": "",
	"TAC": "TOMOGRAFIA", "TC": "TOMOGRAFIA", "CT": "TOMOGRAFIA",
	"TACAR": "TOMOGRAFIA ALTA RESOLUCION TORAX",
	"AXIAL": "", "COMPUTADA": "", "COMPUTARIZADA": "",
	"ECO": "ECOGRAFIA", "US": "ECOGRAFIA", "ULTRASONOGRAFIA": "ECOGRAFIA", "ULTRASONIDO": "ECOGRAFIA",
	"RX": "RADIOGRAFIA", "RADIOGRAFIAS": "RADIOGRAFIA", "COMPARATIVAS": "COMPARATIVA",
	"EMG": "ELECTROMIOGRAFIA", "NC": "NEUROCONDUCCION", "VCN": "NEUROCONDUCCION",
	"NEUROCONDUCCIONES": "NEUROCONDUCCION", "ELECTROMIOGRAFIAS": "ELECTROMIOGRAFIA",
	"EEG": "ELECTROENCEFALOGRAMA", "ELECTROENCEFALOGRAFIA": "ELECTROENCEFALOGRAMA",
	"PETCT":     "PET TOMOGRAFIA",
	"FISIATRIA": "FISICA REHABILITACION", "NEUROPEDIATRIA": "NEUROLOGIA PEDIATRICA",
	// MIEMBRO y EXTREMIDAD son lo mismo en las órdenes y en el catálogo.
	"MMII": "EXTREMIDADES INFERIORES", "MMSS": "EXTREMIDADES SUPERIORES",
	"MIEMBRO": "EXTREMIDAD", "MIEMBROS": "EXTREMIDADES",
	// Errata del catálogo (930860 «ELECTROMIOGRAFÍIA»).
	"ELECTROMIOGRAFIIA": "ELECTROMIOGRAFIA",
	"DER":               "DERECHA", "DCHA": "DERECHA", "DERECHO": "DERECHA", "IZQ": "IZQUIERDA", "IZQUIERDO": "IZQUIERDA",
	"BILAT": "BILATERAL",
	// Sinónimos de región y de columna en las órdenes de Neuro.
	"CEREBRAL": "CEREBRO", "ENCEFALO": "CEREBRO", "CRANEAL": "CEREBRO", "CRANEO": "CEREBRO",
	"TORACICA": "DORSAL", "LUMBOSACRA": "LUMBAR", "COXOFEMORAL": "CADERA", "HIPOFISIS": "SILLA TURCA",
	"PEDIATRICO": "PEDIATRICA", "PEDIATRIA": "PEDIATRICA", "MAMARIA": "MAMA", "MAMAS": "MAMA",
	// Sinónimos anatómicos de las órdenes (revisión, ronda 2): sin ellos la guarda de región escalaría
	// órdenes correctas.
	"TIBIA": "PIERNA", "PERONE": "PIERNA", "HUMERO": "BRAZO", "FEMUR": "MUSLO",
	"ESCROTO": "TESTICULO", "ESCROTAL": "TESTICULO", "ESCROTALES": "TESTICULO", "TESTICULAR": "TESTICULO",
	"PUNO": "MUNECA", "RENAL": "RINON", "RENALES": "RINON", "RINONES": "RINON",
	// Adjetivos de región (ronda 4: el prefijo ya solo vale para el plural).
	"PELVICA": "PELVIS", "PELVICO": "PELVIS", "PELVICAS": "PELVIS", "PELVICOS": "PELVIS",
	"ABDOMINAL": "ABDOMEN", "ABDOMINALES": "ABDOMEN", "CLAVICULARES": "CLAVICULA", "TEMPOROMAXILAR": "TEMPOROMANDIBULAR",
	// 879201 escribe los cuatro segmentos: «TORACICO» es la dorsal, como TORACICA.
	"TORACICO": "DORSAL",
}

// sideLetters son el lado en corto, que se comporta igual que la palabra (ojo y miembro incluidos: OD, MD;
// con puntos, «M.D.», lo une foldProcedureText). En un nombre de columna no son el lado: «CERVICAL Y D» es la
// dorsal, y no se sabe qué quiso decir la orden.
var sideLetters = map[string]string{
	"D": "DERECHA", "OD": "DERECHA", "MD": "DERECHA", "I": "IZQUIERDA", "OI": "IZQUIERDA", "MI": "IZQUIERDA",
}

// retrievalStopWords no aportan a la búsqueda de candidatos (encabezados de remisiones e interconsultas).
var retrievalStopWords = map[string]bool{
	"INTERCONSULTA": true, "REMISION": true, "VALORACION": true, "SERVICIO": true, "ESPECIALIDAD": true,
	"SOLICITUD": true, "ORDEN": true, "REFERENTE": true,
}

// orderNoise son palabras de la orden que no describen el procedimiento (prioridad, proyecciones): no
// necesitan par en el candidato. OBLICUAS sí lo describe: las proyecciones adicionales tienen código propio
// (873444). «3 PROYECCIONES» cuenta vistas, pero al descartar sí se compara (readNameWords).
var orderNoise = map[string]bool{
	"AMB": true, "AMBULATORIO": true, "PRIORITARIO": true, "URGENTE": true,
	"LATERAL": true, "LAT": true, "PROYECCIONES": true, "VISTAS": true,
}

// limbWords dicen dónde se hacen la EMG, la NC o los potenciales («4 EXTREMIDADES»), que el catálogo factura por
// extremidad o nervio: en esos no necesitan par. En lo demás son una región (ECO DOPPLER MIEMBROS).
var limbWords = map[string]bool{"EXTREMIDAD": true, "EXTREMIDADES": true}

// retrievalOnlyNoise no cuentan al buscar candidatos, aunque sí al compararlos: son los que podrían quedarse
// sin par y dejar a un procedimiento sin ningún candidato, y entonces se descartaría sin pasar por el agente.
var retrievalOnlyNoise = map[string]bool{
	"OBLICUA": true, "OBLICUAS": true, "EXTREMIDAD": true, "EXTREMIDADES": true,
	"PROCEDIMIENTO": true, "EXAMEN": true, "ESTUDIO": true,
}

// catalogNoise son palabras del catálogo que anotan la forma de facturar o el equipo, no un procedimiento
// distinto («EN CADA EXTREMIDAD», «CON TRANSDUCTOR DE 7 MHZ O MAS», «ULTRASONOGRAFÍA DIAGNÓSTICA», «CON
// ELECTRODO», «AP, LATERAL»): el nombre leído no tiene que traerlas. Lo que va entre paréntesis o
// corchetes también es anotación (catalogAnnotation). «VASOS» NO lo es («TOMOGRAFÍA COMPUTADA DE VASOS» es
// una angio-TC): «VASOS ARTERIALES/VENOSOS» se reduce a ARTERIAL/VENOSO en orderPhrases.
var catalogNoise = map[string]bool{
	"CADA": true, "UNO": true, "MAS": true, "MHZ": true, "TRANSDUCTOR": true, "DIAGNOSTICA": true,
	"ANALISIS": true, "ELECTRODO": true, "EXTREMIDAD": true, "EXTREMIDADES": true, "NERVIO": true, "LATERAL": true,
}

// catalogAnnotation es lo que el catálogo escribe entre paréntesis o corchetes: «(62)», «(UNO O MAS
// MUSCULOS)», «(CADA NERVIO)», «[CARPOGRAMA]». No obliga al nombre leído a traerlo.
var catalogAnnotation = regexp.MustCompile(`\([^)]*\)|\[[^\]]*\]`)

// separatedUnit es un número y su unidad separados («3 D», «6 H»): sin unirlos, la D se leería como un lado.
var separatedUnit = regexp.MustCompile(`\b(\d+) ([DH])\b`)

// canonicalOrderName deja un nombre de procedimiento en la forma común de las órdenes y el catálogo:
// mayúsculas, sin tildes, con las frases y abreviaturas de orderPhrases y orderAbbreviations.
func canonicalOrderName(text string) string {
	folded := foldOrderName(text)
	raw := strings.FieldsFunc(folded, func(r rune) bool {
		return (r < 'A' || r > 'Z') && (r < '0' || r > '9')
	})
	spine := slices.ContainsFunc(raw, func(w string) bool { return w == "COLUMNA" || isSpineRegion(w) })
	var out []string
	for _, w := range raw {
		if side, ok := sideLetters[w]; ok && !spine {
			out = append(out, side)
			continue
		}
		if exp, ok := orderAbbreviations[w]; ok {
			if exp != "" {
				out = append(out, exp)
			}
			continue
		}
		out = append(out, w)
	}
	return dropVagueWords(strings.Join(out, " "))
}

// dropVagueWords quita PROCEDIMIENTO, EXAMEN y ESTUDIO de un nombre que ya escribe la modalidad: «ESTUDIO DE
// RM CEREBRO» es «RM CEREBRO», y el asunto ya está dicho.
func dropVagueWords(canon string) string {
	words := strings.Fields(canon)
	if !slices.ContainsFunc(words, func(w string) bool { return modalityValues[w] != "" }) {
		return canon
	}
	return strings.Join(slices.DeleteFunc(words, func(w string) bool { return vagueProcedureWords[w] }), " ")
}

// foldOrderName normaliza el nombre (foldProcedureText), lee el contraste y deja en su lugar lo leído
// (readContrast), y reemplaza las frases de las órdenes.
func foldOrderName(text string) string {
	_, folded := readContrast(foldProcedureText(text))
	folded = orderPhrases.Replace(folded)
	return separatedUnit.ReplaceAllString(folded, "$1$2")
}

// CupsNameKey es la forma canónica de un nombre de procedimiento (sin tildes, mayúsculas y abreviaturas
// expandidas): sirve para reconocer el mismo procedimiento sin código escrito de dos maneras.
func CupsNameKey(name string) string { return canonicalOrderName(name) }

// compareReadNames compara dos nombres leídos de la misma orden, para decidir si uno se descarta. Es más
// estricta que la asignación por nombre, porque aquí un error pierde un procedimiento sin que nadie lo vea:
//   - CupsNamesEqual (se descarta): tras quitar el ruido, las mismas palabras en los dos sentidos, con el
//     contraste leído (nunca DESCONOCIDO). Los calificadores y la modalidad también son palabras, así que
//     callar uno no vale por decirlo: «RX TORAX» no es «TORAX», ni «SIMPLE Y CONTRASTE» es «CONTRASTE».
//   - CupsNamesByPrefix (duda, va al agente): coinciden solo por prefijo, o encajan salvo un calificador en
//     silencio (candidateFits sirve para sospechar, no para descartar), o solo los separa un contraste que
//     puede ser el mismo estudio (contrastsMayMatch).
//   - CupsNamesDiffer (se conserva sin código): lo demás, también un nombre sin palabras que lo distingan:
//     «CONSULTA ESPECIALISTA» no dice con qué comparar.
func compareReadNames(a, b string) CupsNameMatch {
	ca, cb := canonicalOrderName(a), canonicalOrderName(b)
	wa, wb := readNameWords(ca), readNameWords(cb)
	if !hasSpecificWord(wa) || !hasSpecificWord(wb) {
		return CupsNamesDiffer
	}
	if allWordsIn(wa, wb, sameWord) && allWordsIn(wb, wa, sameWord) {
		if contrastOf(ca) != contrastUnknown && (!bothSides(wa) || slices.Equal(wa, wb)) && sameQuantityMarks(a, b) {
			return CupsNamesEqual
		}
		return CupsNamesByPrefix
	}
	if allWordsIn(wa, wb, prefixWord) && allWordsIn(wb, wa, prefixWord) {
		return CupsNamesByPrefix
	}
	if candidateFits(ca, b) || candidateFits(cb, a) {
		return CupsNamesByPrefix
	}
	ra, rb := withoutContrast(wa), withoutContrast(wb)
	if contrastsMayMatch(contrastOf(ca), contrastOf(cb)) && allWordsIn(ra, rb, sameWord) && allWordsIn(rb, ra, sameWord) {
		return CupsNamesByPrefix
	}
	return CupsNamesDiffer
}

// withoutContrast quita la lectura del contraste de las palabras de un nombre canónico.
func withoutContrast(words []string) []string {
	return slices.DeleteFunc(slices.Clone(words), func(w string) bool { return contrastValues[w] != "" })
}

// contrastsMayMatch dice si dos lecturas distintas del contraste pueden ser el mismo estudio: una que no se
// entendió, o el contrastado frente al «simple y contrastado». El simple frente al contrastado, no.
func contrastsMayMatch(a, b contrastReading) bool {
	return a == contrastUnknown || b == contrastUnknown ||
		a == contrastWith && b == contrastBoth || a == contrastBoth && b == contrastWith
}

// bothSides dice si el nombre trae los dos lados: entonces importa a qué le toca cada uno («RODILLA DERECHA Y
// CADERA IZQUIERDA» tiene las mismas palabras que «RODILLA IZQUIERDA Y CADERA DERECHA»).
func bothSides(words []string) bool {
	return slices.Contains(words, "DERECHA") && slices.Contains(words, "IZQUIERDA")
}

// sameQuantityMarks dice si dos nombres traen las mismas marcas de cantidad («#1» frente a «#4», o «X2» frente
// a nada). La marca no es parte del nombre, pero al descartar uno se perdería.
func sameQuantityMarks(a, b string) bool {
	return slices.Equal(quantityMarkDigits(a), quantityMarkDigits(b))
}

func quantityMarkDigits(name string) []string {
	var out []string
	for _, m := range quantityMark.FindAllString(accentFolder.Replace(strings.ToUpper(name)), -1) {
		out = append(out, strings.TrimLeft(m, "#XNORUM.:°º \t"))
	}
	sort.Strings(out)
	return out
}

// readNameWords son las palabras de un nombre canónico que cuentan al compararlo con otro leído: las
// distintivas, sin encabezados ni ruido de la orden. Calificadores y números sí cuentan, y PROYECCIONES también:
// suelto puede ser el comienzo de «PROYECCIONES ADICIONALES», y aquí un error pierde un procedimiento.
func readNameWords(canon string) []string {
	var out []string
	for _, w := range distinctiveWords(canon) {
		if !retrievalStopWords[w] && (!orderNoise[w] || w == "PROYECCIONES") {
			out = append(out, w)
		}
	}
	return out
}

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
	// Una lectura sin región frente a varias filas que son ese procedimiento con región es duda, aunque
	// exista la fila a secas: «RX COMPARATIVAS» puede ser cualquiera de las comparativas con región.
	regionless := regionalRows(canonRead, catalog) >= minRegionalRowsForDoubt
	var equal []CupsCandidate
	for _, c := range catalog {
		if CompareCupsNames(canonRead, canonicalOrderName(c.Name)) == CupsNamesEqual {
			equal = append(equal, c)
		}
	}
	if len(equal) == 1 && !regionless {
		return CupsNameResolution{Code: equal[0].Code, Name: equal[0].Name, Via: CupsNameViaEqual, Candidates: equal}
	}
	// La lista corta pone primero los que encajan: un nombre de catálogo con muchas anotaciones (la EMG
	// «en cada extremidad (uno o más músculos)») puede quedar lejos por parecido y ser el único que encaja.
	var fitting, rest []CupsCandidate
	for _, c := range nameCandidates(retrievalRead, catalog) {
		if candidateFits(canonRead, c.Name) {
			fitting = append(fitting, c)
		} else {
			rest = append(rest, c)
		}
	}
	similar := append(append([]CupsCandidate{}, fitting...), rest...)
	res := CupsNameResolution{Candidates: similar[:min(len(similar), maxNameCandidates)]}
	if len(res.Candidates) == 0 || choose == nil || regionless {
		return res
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

// minRegionalRowsForDoubt: con cuántas filas con región de un procedimiento leído sin región la lectura es
// duda. Una sola no basta: la EMG a secas sigue siendo la de cada extremidad aunque exista la de cara.
const minRegionalRowsForDoubt = 2

// regionalRows cuenta las filas del catálogo que son el leído más una región (decisión del dueño, R4-I3):
// el leído no dice región ni segmento de columna, y la fila trae todas las palabras con las que busca
// (retrievalWords: sin el lado, el nivel ni el ruido) y una región.
func regionalRows(canonRead string, catalog []CupsCandidate) int {
	if len(regionGroup.groupValues(canonRead)) > 0 || len(spineGroup.groupValues(canonRead)) > 0 {
		return 0
	}
	readWords := retrievalWords(canonRead)
	if len(readWords) == 0 {
		return 0
	}
	n := 0
	for _, c := range catalog {
		cc := canonicalOrderName(c.Name)
		if len(regionGroup.groupValues(cc)) > 0 && allWordsIn(readWords, distinctiveWords(cc), prefixWord) {
			n++
		}
	}
	return n
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
	readWords := retrievalWords(canonRead)
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

// retrievalWords son las palabras del leído con las que se buscan candidatos. Lo corto se compara pero no
// busca: nunca va a coincidir («RM L4 L5» no tiene L4 en el catálogo), bajaría la fracción de minCandidateScore
// y un procedimiento sin candidatos se descarta sin pasar por el agente. Tampoco buscan el lado (el catálogo
// tiene un solo código para los dos: «EMG MMII D» se quedaba sin candidatos) ni el contraste que no se
// entendió (no está en ningún nombre del catálogo).
func retrievalWords(canonRead string) []string {
	var out []string
	for _, w := range distinctiveWords(canonRead) {
		if retrievalStopWords[w] || orderNoise[w] || retrievalOnlyNoise[w] || spineLevel.MatchString(w) ||
			sideValues[w] != "" || w == string(contrastUnknown) || len(w) < 3 && strings.Trim(w, "0123456789") != "" {
			continue
		}
		out = append(out, w)
	}
	return out
}

// CONTRADICCIÓN ENTRE NOMBRES. Las palabras que cambian el procedimiento se agrupan; dentro de un grupo
// los valores se excluyen (SIMPLE frente a CONTRASTE, PRIMERA VEZ frente a CONTROL, DERECHA frente a
// IZQUIERDA, una región frente a otra, 18-FLUOR frente a PSMA…).

type qualifierGroup struct {
	// values: palabra canónica → valor del grupo.
	values map[string]string
	// plural: además de la palabra exacta, vale su plural (regiones: HOMBRO ~ HOMBROS; pluralWord). Los
	// adjetivos (PELVICA, ABDOMINAL) son sinónimos de orderAbbreviations.
	plural bool
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
	// levels: el grupo son los niveles de columna («L4», «C5»). Como los números, solo cuenta cuando los dos
	// nombres traen niveles: el catálogo no los distingue.
	levels bool
	// exclusive: sus valores se excluyen de verdad (contraste, primera vez/control, pediátrica/adultos,
	// lado, superior/inferior, vía, marcador del PET, nivel de columna y región). Solo estos cuentan para
	// la guarda de los CUPS con código, y solo si los dos nombres traen el grupo y no comparten ningún
	// valor: los sinónimos (CRÁNEO/CEREBRO, LUMBAR/LUMBOSACRA, TIBIA/PIERNA…) se igualan antes.
	exclusive bool
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

// sideValues son los lados, con su valor en el grupo.
var sideValues = words("DERECHA", "IZQUIERDA", "BILATERAL")

// modalityValues son las modalidades, con su valor en el grupo.
var modalityValues = words("RESONANCIA=RM", "TOMOGRAFIA=TC", "ECOGRAFIA=ECO", "RADIOGRAFIA=RX", "PET",
	"ELECTROMIOGRAFIA=EMG", "NEUROCONDUCCION=NC", "ELECTROENCEFALOGRAMA=EEG", "POLISOMNOGRAFIA")

// spineGroup son los segmentos de columna; regionGroup, las regiones anatómicas.
var (
	spineGroup  = qualifierGroup{values: words("CERVICAL", "DORSAL", "LUMBAR", "SACRA", "SACRO=SACRA", "COCCIX"), exclusive: true}
	regionGroup = qualifierGroup{plural: true, exclusive: true, values: words(
		"CEREBRO", "CARA", "ORBITA", "SENOS", "OIDO", "MASTOIDES", "SILLA",
		"CUELLO", "COLUMNA", "TORAX", "ABDOMEN", "PELVIS", "HOMBRO", "BRAZO", "CODO",
		"ANTEBRAZO", "MUNECA", "MANO", "DEDOS", "CADERA", "MUSLO", "RODILLA", "PIERNA",
		"TOBILLO", "PIE", "CALCANEO", "MAXILAR", "MANDIBULA", "PROSTATA", "TIROIDES", "MAMA",
		"RINON", "HIGADO", "VESICULA", "TESTICULO", "SACROILIACA", "CLAVICULA", "ESTERNON",
		"COSTILLAS", "PARRILLA", "TEMPOROMANDIBULAR", "PIES=PIE",
	)}
)

var qualifierGroups = []qualifierGroup{
	{numbers: true},
	{levels: true, exclusive: true},
	{values: modalityValues, readSilentOK: true},
	{values: words("DOPPLER")},
	{values: contrastValues, silentAs: string(contrastSimple), exclusive: true},
	// «VEZ» sola no dice cuál («2 VEZ»): solo PRIMERA (y «1 VEZ», «1A VEZ» por orderPhrases) es la primera vez.
	{values: words("PRIMERA=PRIMERA_VEZ", "CONTROL", "SEGUIMIENTO=CONTROL"), exclusive: true},
	{values: words("PEDIATRICA", "ADULTO", "ADULTOS=ADULTO"), silentAs: "ADULTO", exclusive: true},
	{values: sideValues, readMayAdd: true, exclusive: true},
	// El leído puede precisar superior/inferior cuando el código del catálogo vale para cualquier
	// extremidad (la EMG «en cada extremidad»).
	{values: words("SUPERIOR", "SUPERIORES=SUPERIOR", "INFERIOR", "INFERIORES=INFERIOR"), readMayAdd: true, exclusive: true},
	{values: words("TRANSRECTAL", "TRANSABDOMINAL", "TRANSVAGINAL"), exclusive: true},
	{values: words("TRANSFONTANELAR", "TRANSCRANEAL")},
	{values: words("FLUOR=FDG", "FDG", "FLUORODESOXIGLUCOSA=FDG", "PSMA", "COLINA", "DOTATATE"), exclusive: true},
	{values: words("FUNCIONAL", "TRACTOGRAFIA", "ESPECTROSCOPIA", "PERFUSION")},
	spineGroup,
	regionGroup,
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
		if g.levels {
			if spineLevel.MatchString(w) {
				out[w] = true
			}
			continue
		}
		if v, ok := g.values[w]; ok {
			out[v] = true
			continue
		}
		if !g.plural {
			continue
		}
		for k, v := range g.values {
			if pluralWord(w, k) {
				out[v] = true
			}
		}
	}
	// El PET-TC es un PET.
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
// excluyente (contraste, primera vez/control, pediátrica/adultos, lado, superior/inferior, vía,
// marcador del PET, nivel de columna, región) sin compartir ningún valor. Un nombre que no dice nada de
// un grupo no lo contradice (las órdenes abrevian); la modalidad, el doppler y los números no cuentan. Un
// contraste que no se entendió (contrastUnknown) también es duda: con código, va al agente igual.
func CupsNamesConflict(read, catalog string) bool {
	r, c := canonicalOrderName(read), canonicalOrderName(catalog)
	if levelsContradictRegion(r+" "+c) || contrastOf(r) == contrastUnknown {
		return true
	}
	for _, g := range qualifierGroups {
		if !g.exclusive {
			continue
		}
		rv, cv := g.groupValues(r), g.groupValues(c)
		if len(rv) > 0 && len(cv) > 0 && disjoint(rv, cv) && !contrastCompatible(rv, cv) {
			return true
		}
	}
	return false
}

// candidateFits es la comparación estricta para aceptar un candidato elegido por el modelo (canonRead ya
// canónico; candName, el nombre del catálogo tal cual). Encaja si:
//   - toda palabra de contenido del leído tiene su par en el candidato (igual, en plural, o por una
//     abreviatura o sinónimo de orderAbbreviations; una palabra cortada, NEURO, no es la entera): ANGIOTAC,
//     GAMAGRAFÍA, BIOPSIA o DINÁMICA sin par descartan al candidato;
//   - el candidato no trae palabras de contenido propias que el leído no diga (subtipo, región, vía:
//     FIBRA ÚNICA, CERVICO DORSAL, PÍLORO), salvo sus anotaciones (catalogNoise y lo que va entre
//     paréntesis);
//   - en cada grupo de calificadores los dos dicen lo mismo: el leído puede precisar el lado o
//     superior/inferior, el silencio en el contraste vale SIMPLE y en la pediatría, adultos. El contraste
//     que no se entendió no está en ningún candidato: no encaja con ninguno.
func candidateFits(canonRead, candName string) bool {
	canonCand := canonicalOrderName(candName)
	if levelsContradictRegion(canonRead + " " + canonCand) {
		return false
	}
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
		if (g.numbers || g.levels) && (len(rv) == 0 || len(cv) == 0) {
			continue
		}
		if !sameSet(rv, cv) {
			return false
		}
	}
	readContent := contentWords(canonRead, orderNoise)
	readWords := strings.Fields(canonRead)
	if namesLimbStudy(readWords) {
		readContent = slices.DeleteFunc(readContent, func(w string) bool { return limbWords[w] })
	}
	// 873444 es solo de extremidades: en una columna, el catálogo no distingue las oblicuas. Si el catálogo añade
	// un código de oblicuas de columna, esta regla hay que retirarla.
	if slices.Contains(readWords, "COLUMNA") {
		readContent = slices.DeleteFunc(readContent, func(w string) bool { return w == "OBLICUA" || w == "OBLICUAS" })
	}
	if !allWordsIn(readContent, distinctiveWords(canonCand), pluralWord) {
		return false
	}
	candCore := contentWords(canonicalOrderName(catalogAnnotation.ReplaceAllString(candName, " ")), catalogNoise)
	// Un doppler es de vasos aunque el leído no lo diga: «DOPPLER TESTICULAR» también puede ser el de vasos
	// escrotales (882282), y si encajan los dos, es duda. El doppler ya lo comparó su grupo.
	if slices.Contains(strings.Fields(canonCand), "DOPPLER") {
		candCore = slices.DeleteFunc(candCore, func(w string) bool { return w == "VASOS" })
	}
	return allWordsIn(candCore, distinctiveWords(canonRead), pluralWord)
}

// namesLimbStudy dice si el nombre es de los que se facturan por extremidad o nervio: EMG, NC y potenciales.
func namesLimbStudy(words []string) bool {
	return slices.ContainsFunc(words, func(w string) bool {
		return w == "ELECTROMIOGRAFIA" || w == "NEUROCONDUCCION" || w == "POTENCIALES"
	})
}

// contentWords son las palabras distintivas de un nombre canónico que describen el procedimiento: sin
// encabezados (retrievalStopWords), sin ruido (noise), sin números y sin las palabras de un grupo de
// calificadores (esas las compara su grupo).
func contentWords(canon string, noise map[string]bool) []string {
	var out []string
	for _, w := range distinctiveWords(canon) {
		if retrievalStopWords[w] || noise[w] || strings.Trim(w, "0123456789") == "" || isQualifierWord(w) {
			continue
		}
		out = append(out, w)
	}
	return out
}

// isQualifierWord dice si la palabra es un valor de algún grupo de calificadores.
func isQualifierWord(w string) bool {
	for _, g := range qualifierGroups {
		if g.numbers {
			continue
		}
		if len(g.groupValues(w)) > 0 {
			return true
		}
	}
	return false
}
