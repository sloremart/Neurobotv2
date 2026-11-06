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
	// El PET es siempre de cuerpo entero (asunto 12): la extensión escrita no lo distingue (R6-M1).
	"CUERPO ENTERO", " ", "CUERPO COMPLETO", " ",
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
	// Sinónimos de región y de columna en las órdenes de Neuro. La dorsolumbar son sus dos segmentos («RX COLUMNA
	// DORSAL Y LUMBAR» es 871030, y un nivel cabe en cualquiera de los dos); la sacrocoxígea es el sacro coxis
	// (871050 lo escribe en dos palabras).
	"CEREBRAL": "CEREBRO", "ENCEFALO": "CEREBRO", "CRANEAL": "CEREBRO", "CRANEO": "CEREBRO",
	"TORACICA": "DORSAL", "LUMBOSACRA": "LUMBAR", "COXOFEMORAL": "CADERA", "HIPOFISIS": "SILLA TURCA",
	"DORSOLUMBAR": "DORSAL LUMBAR", "TORACOLUMBAR": "DORSAL LUMBAR",
	"SACROCOXIGEA": "SACRO COXIS", "SACROCOCCIGEA": "SACRO COXIS",
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
	"AMB": true, "AMBULATORIO": true, "PRIORITARIO": true, "PRIORITARIA": true, "CITA": true, "URGENTE": true,
	"LATERAL": true, "LAT": true, "PROYECCIONES": true, "VISTAS": true,
}

// epsNames son las EPS de las órdenes de Neuro, lista CERRADA (ronda 6): «EPS <palabra>» se comía un SIN o un NO.
// Con la palabra EPS delante o detrás («EPS SANITAS», «SANITAS EPS», «NUEVA EPS»).
const epsNames = `SANITAS|SALUD TOTAL|CAPITAL SALUD|FOMAG|COMPENSAR|SURA|FAMISANAR|COOSALUD|COLSANITAS|MEDISANITAS|` +
	`MUTUAL SER|ASMET(?: SALUD)?|EMSSANAR|SAVIA(?: SALUD)?|ALIANSALUD`

// orderTailNoise son colas de la orden que no describen el procedimiento y dejaban una lectura típica sin
// candidatos, y por tanto descartada en silencio (R5-I3, rondas 5b y 6): el plazo («EN 1 MES», «6 MESES», «PARA 2
// SEMANAS»), las sesiones («1 SESION»), la cantidad escrita («CANT 1», «CANTIDAD: 2»), el código del diagnóstico
// («CIE10 M545», «CIE-10: G40»), la EPS de la lista cerrada («EPS SANITAS», «NUEVA EPS»), el número de la orden
// («ORDEN 123456»), «SEGUN EVOLUCION» y «NO POS». El diagnóstico escrito («DX LUMBALGIA») lo corta
// withoutDiagnosis. Se quitan solo al asignar (withoutOrderTails); al descartar entre hojas siguen contando, porque
// otro plazo, otra sesión u otro diagnóstico puede ser otra cita.
var orderTailNoise = []*regexp.Regexp{
	regexp.MustCompile(`\b(?:(?:EN|PARA|A|CADA) )?\d+ (?:MES|MESES|SEMANA|SEMANAS|DIA|DIAS|SESION|SESIONES)\b`),
	regexp.MustCompile(`\bCANT(?:IDAD)?[ .:]*\d+\b`),
	regexp.MustCompile(`\bCIE[ -]?10\b`),
	regexp.MustCompile(`\bNUEVA EPS\b|\bEPS (?:` + epsNames + `)\b|\b(?:` + epsNames + `) EPS\b`),
	regexp.MustCompile(`\bORDEN[ .:#NRO°º]*\d+\b`),
	regexp.MustCompile(`\bSEGUN EVOLUCION\b`),
	regexp.MustCompile(`\bNO POS\b`),
}

// cieCode es un código CIE-10 suelto («M545», «M54.5», «G40», «R51X»): letra y dos o tres cifras. Un nivel de
// columna («T12», «L5») tiene la misma forma y no lo es.
var cieCode = regexp.MustCompile(`^[A-Z]\d{2}\.?\d?X?$`)

// withoutOrderTails quita de la lectura (ya en mayúsculas y sin tildes) las colas de orderTailNoise, el
// diagnóstico escrito tras DX (withoutDiagnosis) y los códigos CIE sueltos. dxDoubt: el diagnóstico trae
// vocabulario de procedimiento y la lectura es duda.
func withoutOrderTails(folded string, vocabulary func(string) bool) (stripped string, dxDoubt bool) {
	for _, tail := range orderTailNoise {
		folded = tail.ReplaceAllString(folded, " ")
	}
	words, dxDoubt := withoutDiagnosis(strings.Fields(folded), vocabulary)
	words = slices.DeleteFunc(words, func(w string) bool {
		return cieCode.MatchString(w) && !spineLevel.MatchString(w)
	})
	return strings.Join(words, " "), dxDoubt
}

// withoutDiagnosis quita «DX» (también «DX:», «DX,», «DX.») y TODO lo que lo sigue, pero solo si ninguna de esas
// palabras —tal cual o en su forma canónica («COXO FEMORAL» es CADERA)— es vocabulario de procedimiento
// (procedureVocabulary). Si alguna lo es, la lectura es duda: todo o nada, cerrado a fallos (ronda 7). Cortar «hasta la primera palabra del procedimiento y conservarla» pegaba al procedimiento la región,
// el lado o la primera vez del diagnóstico («RX COLUMNA DORSAL DX DOLOR LUMBAR» era la dorsolumbar), y cortarlo
// todo se comía la angio, la sedación o la flexión escritas después.
func withoutDiagnosis(words []string, vocabulary func(string) bool) (kept []string, doubt bool) {
	i := slices.IndexFunc(words, func(w string) bool { return strings.Trim(w, ".,:;") == "DX" })
	if i < 0 {
		return words, false
	}
	tail := make([]string, 0, len(words)-i-1)
	for _, w := range words[i+1:] {
		tail = append(tail, strings.Trim(w, ".,:;"))
	}
	doubt = slices.ContainsFunc(slices.Concat(tail, strings.Fields(canonicalOrderName(strings.Join(tail, " ")))), vocabulary)
	// En la duda se devuelve igualmente lo anterior al DX: con eso se buscan los candidatos que ve el agente.
	return words[:i], doubt
}

// procedureRoots son raíces de palabras de tipo o modalidad que el catálogo de prueba no trae tal cual y que
// nunca son un diagnóstico: la angio (ANGIO, ANGIOTAC, ANGIORESONANCIA con una R), la sedación o anestesia
// (asunto 17), la flexión/extensión y la dinámica (871070), el protocolo, la alta resolución (TACAR) y el paquete
// de EMG («PAQ. 2», que es otra fila).
var procedureRoots = []string{"ANGIO", "SEDA", "ANESTES", "FLEXION", "EXTENSION", "DINAMIC", "PROTOCOLO", "RESOLUCION", "PAQ"}

// procedureVocabulary dice, para un catálogo, si una palabra (en mayúsculas, sin tildes) es vocabulario de
// procedimiento: una palabra del contraste o una negación, CONSULTA, un valor de algún grupo de calificadores
// (región, segmento, nivel, lado, modalidad, primera vez/control, pediátrica…), una palabra de algún nombre
// canónico del catálogo —igual, en plural o por prefijo de 5+ letras (DORSALGIA~DORSAL, CONTROLADA~CONTROL)— o
// una de procedureRoots.
func procedureVocabulary(catalog []CupsCandidate) func(string) bool {
	catalogWords := map[string]bool{}
	for _, c := range catalog {
		for _, w := range rowOf(c.Name).words {
			catalogWords[w] = true
		}
	}
	return func(w string) bool {
		// Un número suelto nunca decide: es cantidad, paquete o plazo, no vocabulario («PAQ. 2» tiene el 2).
		if strings.Trim(w, "0123456789") == "" {
			return false
		}
		if procedureWord(w) {
			return true
		}
		for _, root := range procedureRoots {
			if strings.HasPrefix(w, root) {
				return true
			}
		}
		for cw := range catalogWords {
			if pluralWord(w, cw) || prefixWord(w, cw) {
				return true
			}
		}
		return false
	}
}

// procedureWord dice si una palabra habla del procedimiento sin mirar el catálogo: una palabra del contraste o
// una negación, CONSULTA, o un valor de algún grupo de calificadores (región, segmento, nivel, lado, modalidad,
// primera vez/control, pediátrica…). Las abreviaturas y el lado en corto llegan ya expandidos en la forma
// canónica de la cola.
func procedureWord(w string) bool {
	return isContrastWord(w) || w == "CONSULTA" || isQualifierWord(w)
}

// tailsChangeMeaning dice si quitar las colas cambió lo que la lectura dice de algún grupo excluyente —el
// contraste entre ellos— (INVARIANTE de la ronda 6): entonces la cola no era cola, y la lectura es duda.
func tailsChangeMeaning(fullCanon, strippedCanon string) bool {
	for _, g := range qualifierGroups {
		if g.exclusive && !sameSet(g.groupValues(fullCanon), g.groupValues(strippedCanon)) {
			return true
		}
	}
	return false
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
	expanded := make([]string, 0, len(raw))
	for _, w := range raw {
		if exp, ok := orderAbbreviations[w]; ok {
			expanded = append(expanded, strings.Fields(exp)...)
			continue
		}
		expanded = append(expanded, w)
	}
	// El lado en corto se expande sobre las palabras ya canónicas: así una región de columna abreviada
	// (SACROCOXIGEA) también deja la D o la I sin expandir.
	spine := slices.ContainsFunc(expanded, func(w string) bool { return w == "COLUMNA" || isSpineRegion(w) })
	out := make([]string, 0, len(expanded))
	for _, w := range expanded {
		if side, ok := sideLetters[w]; ok && !spine {
			out = append(out, side)
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
	// Son lecturas, no catálogo: se calculan sin guardarlas en la caché (texto de la orden del paciente).
	rowA, rowB := newCatalogRow(a), newCatalogRow(b)
	ca, cb := rowA.canon, rowB.canon
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
	if rowFits(newReadSide(ca), rowB) || rowFits(newReadSide(cb), rowA) {
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
	// El plazo, el diagnóstico, la EPS y los códigos CIE de la orden no buscan ni encajan (R5-I3). Pero quitarlos
	// no puede cambiar lo que la lectura dice de ningún grupo excluyente (ronda 6), y un diagnóstico con
	// vocabulario de procedimiento no se corta (ronda 7): en los dos casos es duda para un agente, con los
	// candidatos de la lectura.
	full := prefoldProcedureText(read)
	stripped, dxDoubt := withoutOrderTails(full, procedureVocabulary(catalog))
	fullCanon, strippedCanon := canonicalOrderName(full), canonicalOrderName(stripped)
	if dxDoubt || tailsChangeMeaning(fullCanon, strippedCanon) {
		// Los candidatos que ve el agente: los de lo que queda sin colas (sin el diagnóstico) y, si no hay nada
		// —la lectura empieza por DX—, los de la lectura entera.
		cands := nameCandidates(strippedCanon, catalog)
		if len(cands) == 0 {
			cands = nameCandidates(fullCanon, catalog)
		}
		return CupsNameResolution{Candidates: cands[:min(len(cands), maxNameCandidates)]}
	}
	res := resolveFoldedRead(stripped, strippedCanon, catalog, interconsulta, choose)
	// Sin ningún candidato tras quitar las colas, la lectura entera antes que un descarte silencioso (ronda 6).
	if len(res.Candidates) == 0 && stripped != full {
		if whole := resolveFoldedRead(full, fullCanon, catalog, interconsulta, choose); len(whole.Candidates) > 0 {
			return whole
		}
	}
	return res
}

// resolveFoldedRead es ResolveCupsByName sobre una lectura ya normalizada (prefoldProcedureText), sin colas y
// con su forma canónica (retrievalRead).
func resolveFoldedRead(
	read, retrievalRead string, catalog []CupsCandidate, interconsulta bool,
	choose func(read string, candidates []CupsCandidate) string,
) CupsNameResolution {
	// La búsqueda de candidatos usa solo lo leído; el sufijo de primera vez de una interconsulta cuenta
	// para el nombre igual y para encajar, no para encontrar candidatos de otra especialidad.
	canonRead := retrievalRead
	if interconsulta {
		canonRead += firstVisitSuffix
	}
	// Una lectura sin región frente a varias filas que son ese procedimiento con región es duda, aunque
	// exista la fila a secas: «RX COMPARATIVAS» puede ser cualquiera de las comparativas con región.
	regionless := regionalRows(canonRead, catalog) >= minRegionalRowsForDoubt
	side := newReadSide(canonRead)
	var equal []CupsCandidate
	for _, c := range catalog {
		// Las dos formas ya son canónicas: la pareja se compara sin volver a canonizar (CompareCupsNames).
		row := rowOf(c.Name)
		if compareNameWords(side.words, row.words, canonRead+" "+row.canon) == CupsNamesEqual {
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
		if rowFits(side, rowOf(c.Name)) {
			fitting = append(fitting, c)
		} else {
			rest = append(rest, c)
		}
	}
	similar := append(append([]CupsCandidate{}, fitting...), rest...)
	// Sin ningún candidato se descartaría en silencio: si la lectura acaba en un motivo («EMG MMII POR
	// DOLOR»), se quita desde el último POR y lo que queda sigue el camino normal (R5-I3).
	if len(similar) == 0 {
		if i := strings.LastIndex(read+" ", " POR "); i > 0 {
			return resolveFoldedRead(read[:i], canonicalOrderName(read[:i]), catalog, interconsulta, choose)
		}
	}
	res := CupsNameResolution{Candidates: similar[:min(len(similar), maxNameCandidates)]}
	if len(res.Candidates) == 0 || choose == nil || regionless {
		return res
	}
	// Sin un único candidato que encaje no hay nada que confirmar: no se gasta la llamada. Tampoco si lo que
	// ese candidato pone entre paréntesis es lo que lo distingue de otra fila (R5-M2).
	if len(fitting) != 1 || !containsCode(res.Candidates, fitting[0].Code) || annotationDistinguishes(canonRead, fitting[0], catalog) {
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

// regionalRows cuenta las filas del catálogo que son el leído MÁS una región (decisión del dueño, R4-I3 y
// R5-I2): el leído no dice región ni segmento de columna, y la fila trae una región y, fuera de ella, del lado, el
// nivel, el ruido y sus anotaciones (lo entre paréntesis y catalogNoise), las mismas palabras que el leído
// (regionalRowWords, pluralWord en los dos sentidos). Otra anatomía o subtipo no cuenta: «MAXILAR INFERIOR» no es
// «RX de miembro inferior con región», ni «MEDICION DE MIEMBROS INFERIORES … (PIES CON APOYO)»; y «RM CEREBRO» no
// es «RM PERFUSION con región».
func regionalRows(canonRead string, catalog []CupsCandidate) int {
	if len(regionGroup.groupValues(canonRead)) > 0 || len(spineGroup.groupValues(canonRead)) > 0 {
		return 0
	}
	readWords := regionalRowWords(distinctiveWords(canonRead), orderNoise)
	if len(readWords) == 0 {
		return 0
	}
	n := 0
	for _, c := range catalog {
		row := rowOf(c.Name)
		if len(regionGroup.groupValues(row.core)) == 0 {
			continue
		}
		rowWords := regionalRowWords(row.coreWords, catalogNoise)
		if allWordsIn(readWords, rowWords, pluralWord) && allWordsIn(rowWords, readWords, pluralWord) {
			n++
		}
	}
	return n
}

// regionalRowWords son las palabras con que se compara una fila con región frente al leído sin región: de las
// distintivas, sin encabezados, ruido, números, nivel, lado ni la región o el segmento. La modalidad y los demás
// calificadores (PERFUSION, CONTRASTE, PEDIATRICA…) sí cuentan: cambian el procedimiento.
func regionalRowWords(words []string, noise map[string]bool) []string {
	var out []string
	for _, w := range words {
		if retrievalStopWords[w] || noise[w] || strings.Trim(w, "0123456789") == "" || spineLevel.MatchString(w) ||
			sideValues[w] != "" || len(regionGroup.groupValues(w)) > 0 || len(spineGroup.groupValues(w)) > 0 {
			continue
		}
		out = append(out, w)
	}
	return out
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
		catWords := rowOf(c.Name).words
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

// spineGroup son los segmentos de columna, ya canónicos (TORACICA es DORSAL, LUMBOSACRA es LUMBAR, DORSOLUMBAR es
// «DORSAL LUMBAR» y SACROCOXIGEA es «SACRO COXIS», por orderAbbreviations): la única fuente para la guarda y para
// los niveles (isSpineRegion, levelFitsRegion). regionGroup son las regiones anatómicas; el sacro y el coxis son
// además una región propia, porque 871050 «SACRO COXIS» se escribe sin COLUMNA y frente a «COLUMNA VERTEBRAL
// TOTAL» o «DINÁMICA DE COLUMNA» (a un dígito) es otra región; ESTERNOCLAVICULAR, frente a TORAX y ESTERNON
// (R5-C2).
var (
	spineGroup = qualifierGroup{exclusive: true, values: words(
		"CERVICAL", "DORSAL", "LUMBAR", "SACRO", "SACRA=SACRO", "COXIS", "COCCIX=COXIS",
	)}
	regionGroup = qualifierGroup{plural: true, exclusive: true, values: words(
		"CEREBRO", "CARA", "ORBITA", "SENOS", "OIDO", "MASTOIDES", "SILLA",
		"CUELLO", "COLUMNA", "TORAX", "ABDOMEN", "PELVIS", "HOMBRO", "BRAZO", "CODO",
		"ANTEBRAZO", "MUNECA", "MANO", "DEDOS", "CADERA", "MUSLO", "RODILLA", "PIERNA",
		"TOBILLO", "PIE", "CALCANEO", "MAXILAR", "MANDIBULA", "PROSTATA", "TIROIDES", "MAMA",
		"RINON", "HIGADO", "VESICULA", "TESTICULO", "SACROILIACA", "CLAVICULA", "ESTERNON",
		"COSTILLAS", "PARRILLA", "TEMPOROMANDIBULAR", "PIES=PIE",
		"SACRO", "SACRA=SACRO", "COXIS=SACRO", "COCCIX=SACRO", "ESTERNOCLAVICULAR",
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
	return rowFits(newReadSide(canonRead), newCatalogRow(candName))
}

// rowFits es candidateFits con las formas de la lectura y del candidato ya calculadas.
func rowFits(read *readSide, cand *catalogRow) bool {
	if levelsContradictRegion(read.canon + " " + cand.canon) {
		return false
	}
	for i, g := range qualifierGroups {
		rv, cv := read.groups[i], cand.groups[i]
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
	if !allWordsIn(read.content, cand.words, pluralWord) {
		return false
	}
	candCore := contentOf(cand.coreWords, catalogNoise)
	// Un doppler es de vasos aunque el leído no lo diga: «DOPPLER TESTICULAR» también puede ser el de vasos
	// escrotales (882282), y si encajan los dos, es duda. El doppler ya lo comparó su grupo.
	if containsWord(strings.Fields(cand.canon), "DOPPLER") {
		candCore = deleteWords(candCore, func(w string) bool { return w == "VASOS" })
	}
	return allWordsIn(candCore, read.words, pluralWord)
}

// familyGroups son los grupos que dicen qué estudio es y dónde: dos filas de la misma familia los comparten.
var familyGroups = []qualifierGroup{{values: modalityValues}, spineGroup, regionGroup}

// annotationDistinguishes dice si lo que el candidato pone entre paréntesis nombra un subtipo o una subregión que
// el leído no dice y que otra fila de la misma familia (misma modalidad y región, y las palabras de contenido del
// candidato) escribe en su nombre: 879523-1 «TOMOGRAFIA … DE MIEMBROS INFERIORES (AXIALES DE ROTULA O LONGITUD DE
// MIEMBROS INFERIORES) - PEDIATRICA» frente a 879523, que lo escribe sin paréntesis. Entonces la anotación sí
// distingue y asignar «TAC MIEMBROS INFERIORES PEDIATRICA» sería elegir por el leído: es duda (R5-M2). Las
// anotaciones de facturación o de equipo («(54)», «(UNO O MAS MUSCULOS)», «(TEJIDOS BLANDOS)») no tienen esa
// otra fila.
func annotationDistinguishes(canonRead string, cand CupsCandidate, catalog []CupsCandidate) bool {
	notes := strings.Join(catalogAnnotation.FindAllString(cand.Name, -1), " ")
	readWords := distinctiveWords(canonRead)
	missing := slices.DeleteFunc(contentWords(canonicalOrderName(notes), catalogNoise), func(w string) bool {
		return slices.ContainsFunc(readWords, func(r string) bool { return pluralWord(w, r) })
	})
	if len(missing) == 0 {
		return false
	}
	candRow := rowOf(cand.Name)
	core := contentOf(candRow.coreWords, catalogNoise)
	for _, other := range catalog {
		if other.Code == cand.Code {
			continue
		}
		otherRow := rowOf(other.Name)
		if !allWordsIn(core, otherRow.coreWords, pluralWord) || !allWordsIn(missing, otherRow.coreWords, pluralWord) {
			continue
		}
		sameFamily := !slices.ContainsFunc(familyGroups, func(g qualifierGroup) bool {
			return !sameSet(g.groupValues(candRow.core), g.groupValues(otherRow.core))
		})
		if sameFamily {
			return true
		}
	}
	return false
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
	return contentOf(distinctiveWords(canon), noise)
}

// contentOf es contentWords sobre las palabras distintivas ya partidas.
func contentOf(words []string, noise map[string]bool) []string {
	var out []string
	for _, w := range words {
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
