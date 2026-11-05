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
//     canónicos: sin tildes, con las abreviaturas de las órdenes expandidas) → se asigna.
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

// orderPhrases se reemplazan antes de partir en palabras (frases de las órdenes con un solo sentido).
// strings.Replacer toma, en cada posición, la primera frase de la lista que coincide: «CON Y SIN
// CONTRASTE» y sus variantes van ANTES de «SIN CONTRASTE», que si no se comería su CONTRASTE. Son un estudio
// simple y contrastado: quedan como «SIMPLE Y CONTRASTE» (el grupo del contraste lo lee como CONTRASTE y el
// nombre igual distingue el «simple y con contraste» del «con contraste»).
var orderPhrases = strings.NewReplacer(
	"CON Y SIN MEDIO DE CONTRASTE", " SIMPLE Y CONTRASTE ", "SIN Y CON MEDIO DE CONTRASTE", " SIMPLE Y CONTRASTE ",
	"CON Y SIN CONTRASTE", " SIMPLE Y CONTRASTE ", "SIN Y CON CONTRASTE", " SIMPLE Y CONTRASTE ",
	"CON/SIN CONTRASTE", " SIMPLE Y CONTRASTE ", "SIN/CON CONTRASTE", " SIMPLE Y CONTRASTE ",
	"CON / SIN CONTRASTE", " SIMPLE Y CONTRASTE ", "C/S CONTRASTE", " SIMPLE Y CONTRASTE ",
	"CON Y SIN GADOLINIO", " SIMPLE Y CONTRASTE ", "SIN Y CON GADOLINIO", " SIMPLE Y CONTRASTE ",
	"SIMPLE Y CON MEDIO DE CONTRASTE", " SIMPLE Y CONTRASTE ",
	"SIN MEDIO DE CONTRASTE", " SIMPLE ", "SIN CONTRASTE", " SIMPLE ", "MEDIO DE CONTRASTE", " CONTRASTE ",
	"TOMOGRAFIA POR EMISION DE POSITRONES", " PET ",
	"VASOS ARTERIALES", " ARTERIAL ", "VASOS VENOSOS", " VENOSO ", "COXO-FEMORAL", " CADERA ", "COXO FEMORAL", " CADERA ",
	"1A VEZ", " PRIMERA VEZ ", "1RA VEZ", " PRIMERA VEZ ", "1ª VEZ", " PRIMERA VEZ ", "1° VEZ", " PRIMERA VEZ ",
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
	"RX":  "RADIOGRAFIA",
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
	// El lado en corto se comporta igual que la palabra (ojo y miembro incluidos: OD, MD).
	"D": "DERECHA", "OD": "DERECHA", "MD": "DERECHA", "I": "IZQUIERDA", "OI": "IZQUIERDA", "MI": "IZQUIERDA",
	"BILAT": "BILATERAL",
	// Sinónimos de región y de columna en las órdenes de Neuro.
	"CEREBRAL": "CEREBRO", "ENCEFALO": "CEREBRO", "CRANEAL": "CEREBRO", "CRANEO": "CEREBRO",
	"TORACICA": "DORSAL", "LUMBOSACRA": "LUMBAR", "COXOFEMORAL": "CADERA", "HIPOFISIS": "SILLA TURCA",
	"CONTRASTADA": "CONTRASTE", "CONTRASTADO": "CONTRASTE", "GADOLINIO": "CONTRASTE",
	"PEDIATRICO": "PEDIATRICA", "PEDIATRIA": "PEDIATRICA", "MAMARIA": "MAMA", "MAMAS": "MAMA",
	// Sinónimos anatómicos de las órdenes (revisión, ronda 2): sin ellos la guarda de región escalaría
	// órdenes correctas.
	"TIBIA": "PIERNA", "PERONE": "PIERNA", "HUMERO": "BRAZO", "FEMUR": "MUSLO",
	"ESCROTO": "TESTICULO", "ESCROTAL": "TESTICULO", "ESCROTALES": "TESTICULO", "TESTICULAR": "TESTICULO",
	"PUNO": "MUNECA", "RENAL": "RINON", "RENALES": "RINON", "RINONES": "RINON",
}

// retrievalStopWords no aportan a la búsqueda de candidatos (encabezados de remisiones e interconsultas).
var retrievalStopWords = map[string]bool{
	"INTERCONSULTA": true, "REMISION": true, "VALORACION": true, "SERVICIO": true, "ESPECIALIDAD": true,
	"SOLICITUD": true, "ORDEN": true, "REFERENTE": true,
}

// orderNoise son palabras de la orden que no describen el procedimiento (prioridad, proyecciones): no
// necesitan par en el candidato.
var orderNoise = map[string]bool{
	"AMB": true, "AMBULATORIO": true, "PRIORITARIO": true, "URGENTE": true,
	"LATERAL": true, "LAT": true, "OBLICUA": true, "OBLICUAS": true, "PROYECCIONES": true, "VISTAS": true,
	// Dónde se hace la EMG o la NC («4 EXTREMIDADES»): el catálogo la factura por extremidad o nervio.
	"EXTREMIDAD": true, "EXTREMIDADES": true,
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

// slashContrast son las abreviaturas con barra del contraste: «C/C», «C/ CONTRASTE», «C/CTE», «C/MEDIO»
// (con contraste) y «S/C», «S/CONTRASTE» (sin contraste). Van antes de partir en palabras, porque la
// partición descarta las palabras de una letra y el contraste se leería como silencio (SIMPLE). Con
// límites de palabra, para no tocar «TAC/C…».
var slashContrast = regexp.MustCompile(`\b([CS]) ?/ ?(C|CTE|CONTRASTE|MEDIO)\b`)

// unknownSlash son letras sueltas pegadas a una barra que no son una abreviatura conocida («C/X»): su
// sentido se pierde al partir en palabras.
var unknownSlash = regexp.MustCompile(`\b[A-Z] ?/|/ ?[A-Z]\b`)

// slashLetters son dos letras sueltas con una barra en medio («C/X»): no se sabe qué dicen. No son palabras
// del nombre; lo que callan lo cubre hasUnknownSlash.
var slashLetters = regexp.MustCompile(`\b[A-Z] ?/ ?[A-Z]\b`)

// contrastEither son las frases que dejan el contraste abierto («CON O SIN CONTRASTE», «SIMPLE O CONTRASTADO»):
// no dicen cuál de los estudios es. Van antes de orderPhrases, cuyo «SIN CONTRASTE» las leería como SIMPLE.
var contrastEither = regexp.MustCompile(`\b(?:CON|SIN|SIMPLE) (?:O|Y/O) (?:CON |SIN )?(?:MEDIO DE )?(?:CONTRAST[A-Z]*|GADOLINIO)\b`)

// unknownContrastWord reemplaza a contrastEither: una palabra sin par en ningún nombre, para que el
// procedimiento no se dé por igual ni encaje con nadie, y vaya al agente.
const unknownContrastWord = " CONTRASTEABIERTO "

// negatedContrast es el contraste negado por delante, con o sin CON y MEDIO DE: «NO CONTRASTADO», «NO CON
// CONTRASTE», «SIN GADOLINIO». Va después de partir las abreviaturas con barra («NO C/C»).
var negatedContrast = regexp.MustCompile(`\b(?:NO|SIN)\s+(?:CON\s+)?(?:MEDIO\s+DE\s+)?(?:CONTRAST[A-Z]*|GADOLINIO)\b`)

// contrastAnsweredNo es el contraste negado al final, como respuesta: «CONTRASTE: NO», «CONTRASTADO (NO)». Solo
// al final: «CONTRASTE NO IONICO» sí lleva contraste.
var contrastAnsweredNo = regexp.MustCompile(`\bCONTRAST[A-Z]*\W*NO\W*$`)

// danglingWith es un nombre cortado en «CON»: CON no cuenta como palabra y el contraste se leería como
// silencio (SIMPLE). unknownContrastWord lo manda al agente.
var danglingWith = regexp.MustCompile(`\bCON\W*$`)

// separatedUnit es un número y su unidad separados («3 D», «6 H»): sin unirlos, la D se leería como un lado.
var separatedUnit = regexp.MustCompile(`\b(\d+) ([DH])\b`)

// canonicalOrderName deja un nombre de procedimiento en la forma común de las órdenes y el catálogo:
// mayúsculas, sin tildes, con las frases y abreviaturas de orderPhrases y orderAbbreviations.
func canonicalOrderName(text string) string {
	folded := foldOrderName(text)
	folded = slashLetters.ReplaceAllString(folded, " ")
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

// foldOrderName pasa a mayúsculas sin tildes y reemplaza las frases y abreviaturas con barra del contraste.
func foldOrderName(text string) string {
	folded := contrastEither.ReplaceAllString(foldProcedureText(text), unknownContrastWord)
	folded = orderPhrases.Replace(folded)
	folded = slashContrast.ReplaceAllStringFunc(folded, func(m string) string {
		if strings.HasPrefix(m, "S") {
			return " SIMPLE "
		}
		return " CONTRASTE "
	})
	folded = negatedContrast.ReplaceAllString(folded, " SIMPLE ")
	folded = contrastAnsweredNo.ReplaceAllString(folded, " SIMPLE ")
	folded = danglingWith.ReplaceAllString(folded, unknownContrastWord)
	return separatedUnit.ReplaceAllString(folded, "$1$2")
}

// hasUnknownSlash dice si el nombre trae letras sueltas con barra que no se entendieron («C/X»): entonces
// el contraste no se puede dar por silencio.
func hasUnknownSlash(text string) bool {
	return unknownSlash.MatchString(foldOrderName(text))
}

// CupsNameKey es la forma canónica de un nombre de procedimiento (sin tildes, mayúsculas y abreviaturas
// expandidas): sirve para reconocer el mismo procedimiento sin código escrito de dos maneras.
func CupsNameKey(name string) string { return canonicalOrderName(name) }

// compareReadNames compara dos nombres leídos de la misma orden, para decidir si uno se descarta. Es más
// estricta que la asignación por nombre, porque aquí un error pierde un procedimiento sin que nadie lo vea:
//   - CupsNamesEqual (se descarta): tras quitar el ruido, las mismas palabras en los dos sentidos. Los
//     calificadores y la modalidad también son palabras, así que callar uno no vale por decirlo: «RX TORAX»
//     no es «TORAX», ni «SIMPLE Y CONTRASTE» es «CONTRASTE».
//   - CupsNamesByPrefix (duda, va al agente): coinciden solo por prefijo, o encajan salvo un calificador en
//     silencio (candidateFits sirve para sospechar, no para descartar).
//   - CupsNamesDiffer (se conserva sin código): lo demás, también un nombre sin palabras que lo distingan:
//     «CONSULTA ESPECIALISTA» no dice con qué comparar.
func compareReadNames(a, b string) CupsNameMatch {
	ca, cb := canonicalOrderName(a), canonicalOrderName(b)
	wa, wb := readNameWords(ca), readNameWords(cb)
	if len(wa) == 0 || len(wb) == 0 {
		return CupsNamesDiffer
	}
	if CompareCupsNames(ca, cb) == CupsNamesByPrefix {
		return CupsNamesByPrefix
	}
	unknownA, unknownB := hasUnknownSlash(a), hasUnknownSlash(b)
	if !unknownA && !unknownB && allWordsIn(wa, wb, sameWord) && allWordsIn(wb, wa, sameWord) {
		if (!bothSides(wa) || slices.Equal(wa, wb)) && sameQuantityMarks(a, b) {
			return CupsNamesEqual
		}
		return CupsNamesByPrefix
	}
	if candidateFits(ca, b, unknownA) || candidateFits(cb, a, unknownB) {
		return CupsNamesByPrefix
	}
	return CupsNamesDiffer
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
// distintivas, sin encabezados ni ruido de la orden. Calificadores y números sí cuentan.
func readNameWords(canon string) []string {
	var out []string
	for _, w := range distinctiveWords(canon) {
		if !retrievalStopWords[w] && !orderNoise[w] {
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
	var equal []CupsCandidate
	for _, c := range catalog {
		if CompareCupsNames(canonRead, canonicalOrderName(c.Name)) == CupsNamesEqual {
			equal = append(equal, c)
		}
	}
	if len(equal) == 1 {
		return CupsNameResolution{Code: equal[0].Code, Name: equal[0].Name, Via: CupsNameViaEqual, Candidates: equal}
	}
	// La lista corta pone primero los que encajan: un nombre de catálogo con muchas anotaciones (la EMG
	// «en cada extremidad (uno o más músculos)») puede quedar lejos por parecido y ser el único que encaja.
	// Una abreviatura con barra que no se entendió («C/X») puede decir algo del contraste: no vale como
	// silencio (SIMPLE) y nada encaja por ese lado; solo el nombre igual, arriba, asigna.
	contrastUnknown := hasUnknownSlash(read)
	var fitting, rest []CupsCandidate
	for _, c := range nameCandidates(retrievalRead, catalog) {
		if candidateFits(canonRead, c.Name, contrastUnknown) {
			fitting = append(fitting, c)
		} else {
			rest = append(rest, c)
		}
	}
	similar := append(append([]CupsCandidate{}, fitting...), rest...)
	res := CupsNameResolution{Candidates: similar[:min(len(similar), maxNameCandidates)]}
	if len(res.Candidates) == 0 || choose == nil {
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
// y un procedimiento sin candidatos se descarta sin pasar por el agente.
func retrievalWords(canonRead string) []string {
	var out []string
	for _, w := range distinctiveWords(canonRead) {
		if retrievalStopWords[w] || orderNoise[w] || spineLevel.MatchString(w) || len(w) < 3 && strings.Trim(w, "0123456789") != "" {
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

var qualifierGroups = []qualifierGroup{
	{numbers: true},
	{levels: true, exclusive: true},
	{values: words("RESONANCIA=RM", "TOMOGRAFIA=TC", "ECOGRAFIA=ECO", "RADIOGRAFIA=RX", "PET",
		"ELECTROMIOGRAFIA=EMG", "NEUROCONDUCCION=NC", "ELECTROENCEFALOGRAMA=EEG", "POLISOMNOGRAFIA"), readSilentOK: true},
	{values: words("DOPPLER")},
	{values: words("SIMPLE", "CONTRASTE"), silentAs: "SIMPLE", exclusive: true},
	{values: words("PRIMERA=PRIMERA_VEZ", "VEZ=PRIMERA_VEZ", "CONTROL", "SEGUIMIENTO=CONTROL"), exclusive: true},
	{values: words("PEDIATRICA", "ADULTO", "ADULTOS=ADULTO"), silentAs: "ADULTO", exclusive: true},
	{values: words("DERECHA", "IZQUIERDA", "BILATERAL"), readMayAdd: true, exclusive: true},
	// El leído puede precisar superior/inferior cuando el código del catálogo vale para cualquier
	// extremidad (la EMG «en cada extremidad»).
	{values: words("SUPERIOR", "SUPERIORES=SUPERIOR", "INFERIOR", "INFERIORES=INFERIOR"), readMayAdd: true, exclusive: true},
	{values: words("TRANSRECTAL", "TRANSABDOMINAL", "TRANSVAGINAL"), exclusive: true},
	{values: words("TRANSFONTANELAR", "TRANSCRANEAL")},
	{values: words("FLUOR=FDG", "FDG", "FLUORODESOXIGLUCOSA=FDG", "PSMA", "COLINA", "DOTATATE"), exclusive: true},
	{values: words("FUNCIONAL", "TRACTOGRAFIA", "ESPECTROSCOPIA", "PERFUSION")},
	{values: words("CERVICAL", "DORSAL", "LUMBAR", "SACRA", "SACRO=SACRA", "COCCIX"), exclusive: true},
	{prefix: true, exclusive: true, values: words(
		"CEREBRO", "CARA", "ORBITA", "SENOS", "OIDO", "MASTOIDES", "SILLA",
		"CUELLO", "COLUMNA", "TORAX", "ABDOMEN", "PELVIS", "HOMBRO", "BRAZO", "CODO",
		"ANTEBRAZO", "MUNECA", "MANO", "DEDOS", "CADERA", "MUSLO", "RODILLA", "PIERNA",
		"TOBILLO", "PIE", "CALCANEO", "MAXILAR", "MANDIBULA", "PROSTATA", "TIROIDES", "MAMA",
		"RINON", "HIGADO", "VESICULA", "TESTICULO", "SACROILIACA", "CLAVICULA", "ESTERNON",
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
// excluyente (contraste, primera vez/control, pediátrica/adultos, lado, superior/inferior, vía,
// marcador del PET, nivel de columna, región) sin compartir ningún valor. Un nombre que no dice nada de
// un grupo no lo contradice (las órdenes abrevian); la modalidad, el doppler y los números no cuentan.
func CupsNamesConflict(read, catalog string) bool {
	r, c := canonicalOrderName(read), canonicalOrderName(catalog)
	for _, g := range qualifierGroups {
		if !g.exclusive {
			continue
		}
		rv, cv := g.groupValues(r), g.groupValues(c)
		if len(rv) > 0 && len(cv) > 0 && disjoint(rv, cv) {
			return true
		}
	}
	return false
}

// candidateFits es la comparación estricta para aceptar un candidato elegido por el modelo (canonRead ya
// canónico; candName, el nombre del catálogo tal cual). Encaja si:
//   - toda palabra de contenido del leído tiene su par en el candidato (igual, por prefijo, o por una
//     abreviatura o sinónimo de orderAbbreviations): ANGIOTAC, GAMAGRAFÍA, BIOPSIA o DINÁMICA sin par
//     descartan al candidato;
//   - el candidato no trae palabras de contenido propias que el leído no diga (subtipo, región, vía:
//     FIBRA ÚNICA, CERVICO DORSAL, PÍLORO), salvo sus anotaciones (catalogNoise y lo que va entre
//     paréntesis);
//   - en cada grupo de calificadores los dos dicen lo mismo: el leído puede precisar el lado o
//     superior/inferior, el silencio en el contraste vale SIMPLE (salvo contrastUnknown: el leído traía
//     una abreviatura con barra que no se entendió) y en la pediatría, adultos.
func candidateFits(canonRead, candName string, contrastUnknown bool) bool {
	canonCand := canonicalOrderName(candName)
	for _, g := range qualifierGroups {
		rv, cv := g.groupValues(canonRead), g.groupValues(canonCand)
		if contrastUnknown && g.values["CONTRASTE"] != "" && len(rv) == 0 {
			return false
		}
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
	if !allWordsIn(readContent, distinctiveWords(canonCand), prefixWord) {
		return false
	}
	candCore := contentWords(canonicalOrderName(catalogAnnotation.ReplaceAllString(candName, " ")), catalogNoise)
	return allWordsIn(candCore, distinctiveWords(canonRead), prefixWord)
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
