// Package handlers registra los handlers de cada estado de la máquina de estados del bot.
package handlers

import (
	"strconv"
	"strings"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/session"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
	"github.com/neuro-bot/neuro-bot/internal/statemachine/validators"
	"github.com/neuro-bot/neuro-bot/internal/utils"
)

// optionsFromRows deriva las opciones del agente de las filas de lista que ve el paciente, para
// no duplicar el catálogo: el rótulo es el título de la fila y el payload es su ID, que el
// handler del paso acepta tal cual (llega como postback y como texto).
func optionsFromRows(rows []sm.ListRow) []sm.ReturnOption {
	options := make([]sm.ReturnOption, len(rows))
	for i, r := range rows {
		options[i] = sm.ReturnOption{Label: r.Title, Value: r.ID, Data: r.ID}
	}
	return options
}

// documentTypeOptions son los tipos de documento del catálogo; el código se inyecta como lo
// escribiría el paciente (`/bot resume ASK_DOCUMENT_TYPE CC`). Lo comparten ASK_DOCUMENT_TYPE y
// REG_DOCUMENT_TYPE, que aceptan los mismos códigos.
func documentTypeOptions() []sm.ReturnOption {
	options := make([]sm.ReturnOption, 0, len(documentTypeCatalog))
	for _, d := range documentTypeCatalog {
		options = append(options,
			sm.ReturnOption{Label: d.Label + " (" + d.Code + ")", Value: d.Code, Data: d.Code})
	}
	return options
}

// payloadOption es la opción que inyecta `payload` como lo haría el botón del paciente.
func payloadOption(label, payload string) sm.ReturnOption {
	return sm.ReturnOption{Label: label, Value: payload, Data: payload}
}

// --- Campos escritos del agente de Néctar ---
//
// Cada campo usa EL MISMO validador que el handler de su paso: un dato que el campo acepta nunca
// es uno que el paso le rechazaría al paciente (y al revés). El bot revalida todo: Néctar solo
// comprueba `requerido`, `largo` y `patron` en su consola.

// agentResumeLabel es el rótulo de bot_reanudar en los pasos de campo: es la salida que lleva lo
// escrito.
const agentResumeLabel = "Enviar el dato escrito al bot"

// agentCupsResumeLabel es el mismo rótulo en los pasos de la orden médica.
const agentCupsResumeLabel = "Enviar los CUPS escritos al bot"

// agentTextInput arma el campo requerido de un paso. Con validate, el rechazo es `problem`, que
// también es la ayuda del campo; sin validate se aplica el TextValidate del paso con su ErrorMsg
// (y la ayuda es ese ErrorMsg). Una fecha se publica con formato iso: el selector de la consola
// devuelve AAAA-MM-DD, que parseBirthDate acepta.
func agentTextInput(label, kind string, validate func(string) bool, problem string) sm.ReturnInput {
	in := sm.ReturnInput{Kind: kind, Label: label, Help: problem, Required: true}
	if kind == sm.InputKindDate {
		in.Format = sm.InputFormatISO
	}
	if validate != nil {
		in.Validate = func(_ *session.Session, value string) sm.InputRejection {
			if validate(value) {
				return sm.InputRejection{}
			}
			return sm.Reject(problem)
		}
	}
	return in
}

// asking le pone al campo la pregunta normal de su paso: es lo que el paciente recibe cuando el
// agente lo devuelve sin dato («Volver a preguntarle»), en vez del aviso de error del paso.
func asking(prompt string, in sm.ReturnInput) sm.ReturnInput {
	in.Prompt = prompt
	return in
}

// askAgainOption reanuda el paso sin dato: el handler recibe __resume__, lo rechaza y le vuelve a
// hacer la pregunta al paciente. Solo va en pasos cuyo handler re-pregunta ante una palabra
// reservada (TestAskAgainOnlyWhereTheHandlerRejectsReservedKeywords).
func askAgainOption() sm.ReturnOption {
	return sm.ReturnOption{Label: "Volver a preguntarle al paciente", Value: "repreguntar", NoInput: true}
}

// registerAgentField registra el campo del paso, el rótulo de reanudar y, si las hay, sus
// opciones sin dato (que REEMPLAZAN el menú del paso).
func registerAgentField(step string, in sm.ReturnInput, options ...sm.ReturnOption) {
	sm.RegisterReturnInput(step, in)
	sm.RegisterResumeLabel(step, agentResumeLabel)
	if len(options) > 0 {
		sm.RegisterStaticReturnMenu(step, options...)
	}
}

// noInputPayloadOption es payloadOption sin exigir el campo escrito del paso.
func noInputPayloadOption(label, payload string) sm.ReturnOption {
	o := payloadOption(label, payload)
	o.NoInput = true
	return o
}

// --- Validadores compartidos handler ↔ campo ---

// isNoAnswer dice si el paciente contestó que no tiene el dato (NA y equivalentes).
func isNoAnswer(s string) bool {
	return noResponses[strings.ToLower(strings.TrimSpace(s))]
}

// freeTextAnswer acepta cualquier texto no vacío salvo una palabra reservada (__resume__): un
// paso de texto libre la trata como entrada vacía y nunca la guarda como dato (C12).
func freeTextAnswer(s string) bool {
	return validateNotEmpty(s) && !sm.IsReservedKeyword(strings.TrimSpace(s))
}

// optionalName es un nombre válido o NA.
func optionalName(s string) bool {
	return isNoAnswer(s) || validateName(strings.TrimSpace(s))
}

// optionalPhone es un celular colombiano o NA.
func optionalPhone(s string) bool {
	return isNoAnswer(s) || utils.ParseColombianPhone(strings.TrimSpace(s)) != ""
}

// emailOr acepta un correo (en minúsculas) o una de las respuestas «no tengo» del paso. Cada
// paso pasa SU conjunto: REG_EMAIL usa noResponses y ASK_UPDATE_EMAIL contactEmailNoAnswers.
func emailOr(noAnswers map[string]bool) func(string) bool {
	return func(s string) bool {
		lower := strings.ToLower(strings.TrimSpace(s))
		return noAnswers[lower] || validators.Email(lower)
	}
}

// contactEmailNoAnswers son las respuestas «no tengo» de ASK_UPDATE_EMAIL. No es noResponses:
// ese paso nunca aceptó «ninguna», y el paciente de Bird no ve ningún cambio.
var contactEmailNoAnswers = map[string]bool{
	"no tengo": true, "no": true, "na": true, "n/a": true, "-": true, "ninguno": true,
}

// parseDecimal lee un número con coma o punto decimal.
func parseDecimal(s string) (float64, bool) {
	v, err := strconv.ParseFloat(strings.Replace(strings.TrimSpace(s), ",", ".", 1), 64)
	return v, err == nil
}

// parseCreatinine lee la creatinina en mg/dL: 0 < v ≤ 30.
func parseCreatinine(s string) (float64, bool) {
	v, ok := parseDecimal(s)
	return v, ok && v > 0 && v <= 30
}

func validCreatinine(s string) bool { _, ok := parseCreatinine(s); return ok }

// parseHeightCm lee la estatura en centímetros: 30 ≤ v ≤ 250.
func parseHeightCm(s string) (float64, bool) {
	v, ok := parseDecimal(s)
	return v, ok && v >= 30 && v <= 250
}

func validHeightCm(s string) bool { _, ok := parseHeightCm(s); return ok }

// parseGestationalWeeks lee las semanas de gestación: v > 0.
func parseGestationalWeeks(s string) (float64, bool) {
	v, ok := parseDecimal(s)
	return v, ok && v > 0
}

func validGestationalWeeks(s string) bool { _, ok := parseGestationalWeeks(s); return ok }

// parseExamDate lee la fecha de toma del examen: el formato de parseBirthDate y no futura.
func parseExamDate(s string) (time.Time, bool) {
	d, ok := parseBirthDate(strings.TrimSpace(s))
	return d, ok && !d.After(time.Now())
}

func validExamDate(s string) bool { _, ok := parseExamDate(s); return ok }

// birthDateVerdict es el juicio de REG_BIRTH_DATE; cada rechazo tiene su texto al paciente.
type birthDateVerdict int

const (
	birthDateOK birthDateVerdict = iota
	birthDateUnreadable
	birthDateInFuture
	birthDateBefore1900
)

// judgeBirthDate aplica la regla completa de la fecha de nacimiento.
func judgeBirthDate(s string) (time.Time, birthDateVerdict) {
	d, ok := parseBirthDate(strings.TrimSpace(s))
	switch {
	case !ok:
		return d, birthDateUnreadable
	case d.After(time.Now()):
		return d, birthDateInFuture
	case d.Year() < 1900:
		return d, birthDateBefore1900
	}
	return d, birthDateOK
}

func validBirthDate(s string) bool { _, v := judgeBirthDate(s); return v == birthDateOK }
