package statemachine

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/session"
	"github.com/neuro-bot/neuro-bot/internal/utils"
)

var maxRetries = 3

// sensitiveInputStates: estados donde el input del usuario es PII (número de documento) y NO
// debe persistirse en claro en chat_events / logs (Ley 1581). El valor se enmascara antes de
// adjuntarlo a los eventos (N-9). La validación sigue usando el valor real, solo se enmascara
// lo que se registra.
var sensitiveInputStates = map[string]bool{
	StateAskDocument: true,
}

// maskInputForLog enmascara el input si el estado actual es sensible; si no, lo deja igual.
func maskInputForLog(state, input string) string {
	if sensitiveInputStates[state] {
		return utils.MaskDocument(input)
	}
	return input
}

// maxLastInputRunes es el tope de lo último que el paciente escribió y se le muestra al agente.
const maxLastInputRunes = 120

// lastInputForAgent es el último mensaje del paciente tal como lo ve el agente: enmascarado por
// estado (el documento nunca viaja en claro) y recortado a maxLastInputRunes.
func lastInputForAgent(state, input string) string {
	masked := []rune(maskInputForLog(state, strings.TrimSpace(input)))
	if len(masked) > maxLastInputRunes {
		masked = masked[:maxLastInputRunes]
	}
	return string(masked)
}

// SetMaxRetries configures the maximum retry count (called from main).
func SetMaxRetries(n int) {
	if n > 0 {
		maxRetries = n
	}
}

// EscalationNoticeText es el aviso al paciente cuando una ruta escala SIN texto contextual propio.
// Viaja en el StateResult (un solo mensaje por turno); el handler de escalación ya no hace el
// envío directo redundante que duplicaba el aviso en las rutas que sí tienen texto.
const EscalationNoticeText = "Te voy a conectar con un agente. Un momento por favor..."

// IsAgentResumeTurn dice si este turno es el retorno sin dato de un agente de Néctar.
func IsAgentResumeTurn(sess *session.Session) bool {
	return sess.GetContext(CtxAgentResumeTurn) != ""
}

// ResumeText elige el texto de un paso que valida con reintentos: en el retorno sin dato del
// agente el paciente no escribió nada, así que se le repite solo la pregunta (prompt) y no el
// aviso de error (errMsg).
func ResumeText(sess *session.Session, errMsg, prompt string) string {
	if IsAgentResumeTurn(sess) {
		return prompt
	}
	return errMsg
}

// ResumeRePrompt es el guard de los pasos que buscan el texto en un catálogo: si el turno es el
// retorno sin dato del agente (`__resume__`) NO se busca; el paso re-pregunta con su pregunta normal,
// sin error ni reintento. Devuelve nil en cualquier otro turno (Bird incluido).
func ResumeRePrompt(sess *session.Session, input, prompt string) *StateResult {
	if IsAgentResumeTurn(sess) && IsReservedKeyword(input) {
		return agentResumeReprompt(sess, prompt)
	}
	return nil
}

// stepResumePrompt es la pregunta normal que el campo del agente declaró para el paso
// (ReturnInput.Prompt), o fallback si el paso no la declaró.
func stepResumePrompt(step, fallback string) string {
	if in, ok := returnInputFor(step); ok && in.Prompt != "" {
		return in.Prompt
	}
	return fallback
}

// agentResumeReprompt es la respuesta de un paso que recibe el retorno sin dato del agente: se
// queda donde está con su texto de siempre, sin gastar reintento ni escalar (el paciente no
// escribió nada que pudiera estar mal).
func agentResumeReprompt(sess *session.Session, text string) *StateResult {
	return NewResult(sess.CurrentState).
		WithText(text).
		WithEvent("agent_resume_reprompt", map[string]interface{}{"state": sess.CurrentState})
}

// ValidateWithRetry valida input del usuario con reintentos automáticos.
// Retorna nil si el input es válido (caller debe procesarlo).
// Retorna StateResult si hay error de validación o escalación por reintentos.
func ValidateWithRetry(sess *session.Session, input string, validate func(string) bool, errorMsg string) *StateResult {
	if validate(input) {
		sess.RetryCount = 0
		return nil
	}

	if IsAgentResumeTurn(sess) {
		return agentResumeReprompt(sess, ResumeText(sess, errorMsg, stepResumePrompt(sess.CurrentState, errorMsg)))
	}

	sess.RetryCount++

	if sess.RetryCount >= maxRetries {
		sess.RetryCount = 0
		// Escalate to agent first; if agent unavailable, escalation handler falls back to FALLBACK_MENU
		return NewResult(StateEscalateToAgent).
			WithText(EscalationNoticeText).
			WithContext(CtxEscalationReason, EscalationReasonMaxRetries).
			WithContext(CtxEscalationLastInput, lastInputForAgent(sess.CurrentState, input)).
			WithEvent("max_retries_reached", map[string]interface{}{
				"state":      sess.CurrentState,
				"retries":    maxRetries,
				"last_input": maskInputForLog(sess.CurrentState, input),
			})
	}

	return NewResult(sess.CurrentState).
		WithText(errorMsg).
		WithEvent("invalid_input", map[string]interface{}{
			"state": sess.CurrentState,
			"retry": sess.RetryCount,
			"input": maskInputForLog(sess.CurrentState, input),
		})
}

// ValidateButtonResponse valida que la respuesta sea uno de los postback esperados.
// Acepta: postbacks exactos, texto numérico (1, 2, 3...), o texto que coincida
// con un payload (case-insensitive, ej: "casado" → "CASADO").
// Retorna (nil, payload) si válido; (result, "") si inválido.
func ValidateButtonResponse(sess *session.Session, msg bird.InboundMessage, validPayloads ...string) (*StateResult, string) {
	// Si es postback, verificar que sea válido
	if msg.IsPostback {
		for _, valid := range validPayloads {
			if msg.PostbackPayload == valid {
				sess.RetryCount = 0
				return nil, valid
			}
		}
	}

	// También aceptar texto que coincida con números (1, 2, 3)
	input := strings.TrimSpace(msg.Text)
	for i, valid := range validPayloads {
		if input == fmt.Sprintf("%d", i+1) {
			sess.RetryCount = 0
			return nil, valid
		}
	}

	// También aceptar texto que coincida con un payload (case-insensitive)
	for _, valid := range validPayloads {
		if strings.EqualFold(input, valid) {
			sess.RetryCount = 0
			return nil, valid
		}
	}

	if IsAgentResumeTurn(sess) {
		return agentResumeReprompt(sess, "Por favor selecciona una de las opciones disponibles."), ""
	}

	// Input no válido → retry. Registrar SIEMPRE el texto (enmascarado por estado): es la materia
	// prima del análisis de fricción (§8.2 — el 70% de invalid_input llegaba sin texto porque los
	// estados de botones no lo guardaban). Si fue postback, se registra el payload.
	loggedInput := input
	if loggedInput == "" && msg.IsPostback {
		loggedInput = msg.PostbackPayload
	}
	loggedInput = maskInputForLog(sess.CurrentState, loggedInput)

	sess.RetryCount++
	if sess.RetryCount >= maxRetries {
		sess.RetryCount = 0
		// Escalate to agent first; if agent unavailable, escalation handler falls back to FALLBACK_MENU
		return NewResult(StateEscalateToAgent).
			WithText(EscalationNoticeText).
			WithContext(CtxEscalationReason, EscalationReasonMaxRetries).
			WithContext(CtxEscalationLastInput, lastInputForAgent(sess.CurrentState, loggedInput)).
			WithEvent("max_retries_reached", map[string]interface{}{
				"state":      sess.CurrentState,
				"last_input": loggedInput,
			}), ""
	}

	return NewResult(sess.CurrentState).
		WithText("Por favor selecciona una de las opciones disponibles.").
		WithEvent("invalid_input", map[string]interface{}{
			"state": sess.CurrentState,
			"retry": sess.RetryCount,
			"input": loggedInput,
		}), ""
}

// RetryOrEscalate incrementa RetryCount y retorna retry o escalación.
// Reemplaza el patrón manual de sess.RetryCount++ / if >= 3 { escalate }.
// Siempre retorna un *StateResult (nunca nil).
func RetryOrEscalate(sess *session.Session, errorMsg string) *StateResult {
	if IsAgentResumeTurn(sess) {
		return agentResumeReprompt(sess, ResumeText(sess, errorMsg, stepResumePrompt(sess.CurrentState, errorMsg)))
	}

	sess.RetryCount++

	if sess.RetryCount >= maxRetries {
		sess.RetryCount = 0
		// Escalate to agent first; if agent unavailable, escalation handler falls back to FALLBACK_MENU
		return NewResult(StateEscalateToAgent).
			WithText(EscalationNoticeText).
			WithContext(CtxEscalationReason, EscalationReasonMaxRetries).
			WithEvent("max_retries_reached", map[string]interface{}{
				"state":   sess.CurrentState,
				"retries": maxRetries,
			})
	}

	return NewResult(sess.CurrentState).
		WithText(errorMsg).
		WithEvent("invalid_input", map[string]interface{}{
			"state": sess.CurrentState,
			"retry": sess.RetryCount,
		})
}

// SearchOutcome indica el resultado de validar un conteo de búsqueda.
type SearchOutcome int

const (
	SearchNone     SearchOutcome = iota // 0 resultados
	SearchExact                         // 1 resultado
	SearchMultiple                      // 2+ resultados dentro del límite
	SearchTooMany                       // Demasiados resultados
)

// ValidateSearchCount valida el conteo de resultados de una búsqueda.
// Retorna (outcome, nil) si hay resultados válidos (Exact o Multiple).
// Retorna (outcome, *StateResult) si no hay resultados o hay demasiados (con retry/escalación).
func ValidateSearchCount(sess *session.Session, count, maxDisplay int, noMatchMsg, tooManyMsg string) (SearchOutcome, *StateResult) {
	switch {
	case count == 0:
		return SearchNone, RetryOrEscalate(sess, noMatchMsg)
	case count == 1:
		return SearchExact, nil
	case count > maxDisplay:
		return SearchTooMany, RetryOrEscalate(sess, tooManyMsg)
	default:
		return SearchMultiple, nil
	}
}

// CtxAgentInputRejected contará cuántas veces, en ESTA sesión, un dato escrito por un agente
// humano no se pudo usar. Lo escribirá el worker al re-escalar y alimentará el número de
// intento de la nota para el siguiente agente.
const CtxAgentInputRejected = "agent_input_rejected_count"

// CtxAgentInputRejection guardará el rechazo ENTERO que cause la re-escalada en curso, en JSON
// (PersistedRejection). El worker lo escribirá al re-escalar y el handler de escalación lo leerá
// al armar el sobre: son turnos distintos y la sesión es lo único que los une. Es UNA clave y no
// cuatro para que la escritura no pueda quedar a medias: el `rechazo` del sobre es todo-o-nada.
const CtxAgentInputRejection = "agent_input_rejection"

// PersistedRejection es la forma con que el rechazo cruza de un turno al otro. Las claves van en
// español porque son las del cable, y el tipo vive AQUÍ para que quien escribe y quien lee no
// tengan dos copias que desincronizar.
//
// ⚠️ `Reason` y `Echo` NO SE TRAZAN: el eco es lo que una persona escribió sobre un paciente.
type PersistedRejection struct {
	Reason  string `json:"motivo"`
	Attempt int    `json:"intento,omitempty"`
	Echo    string `json:"eco,omitempty"`
	Field   string `json:"campo,omitempty"`
	Piece   string `json:"pieza,omitempty"`
}

// EncodeRejection rinde el rechazo para la sesión. El error se devuelve y no se traga: el
// llamador decide, y lo único que se pierde si falla es la clave tipada (la nota del resumen
// viaja igual).
func EncodeRejection(r PersistedRejection) (string, error) {
	raw, err := json.Marshal(r)
	return string(raw), err
}

// DecodeRejection lee lo que EncodeRejection guardó. El segundo retorno es «hay rechazo»:
// ausente, ilegible o sin motivo son lo mismo para quien arma el sobre (no se emite), porque
// `motivo` es la única clave que el esquema exige. Un JSON ilegible NO se traza: llevaría
// dentro el motivo y el eco.
func DecodeRejection(raw string) (PersistedRejection, bool) {
	if raw == "" {
		return PersistedRejection{}, false
	}
	var r PersistedRejection
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		slog.Warn("no se pudo leer el rechazo persistido, la escalación sale sin `rechazo`")
		return PersistedRejection{}, false
	}
	return r, r.Reason != ""
}

// RejectedInputMarker es el literal de cable con el que empieza la línea `Nota:` del resumen
// cuando un dato devuelto por el agente no sirvió. Néctar lo busca tal cual para destacar el
// aviso; si cambia hay que avisarles antes.
const RejectedInputMarker = "EL DATO QUE SE DEVOLVIO NO SE PUDO USAR"

// RejectedInputNote arma la nota completa de un dato rechazado. Vive aquí para que quien la
// escribe y los archivos dorados que la fijan usen la MISMA plantilla.
func RejectedInputNote(attempt int, received, reason string) string {
	return fmt.Sprintf(
		RejectedInputMarker+" (intento %d). Se recibio: %q. Motivo: %s "+
			"Vuelva a devolver la conversacion escribiendo el valor correcto.",
		attempt, received, reason)
}

// WithoutConnectionNotice quita del turno el aviso GENÉRICO de conexión (EscalationNoticeText, el
// que usan el interceptor de palabra clave, el menú post-acción, los helpers y la recuperación IA)
// y deja todo lo demás en su orden. Lo usa el pre-envío de Néctar cuando la escalación se encola
// para el próximo día hábil: «un momento por favor» contradice al aviso del próximo día, pero la
// confirmación de una cita ya agendada o el motivo propio del caller tienen que llegar.
// Se identifica por la constante, nunca por heurística sobre el texto.
func WithoutConnectionNotice(msgs []OutboundMessage) []OutboundMessage {
	out := make([]OutboundMessage, 0, len(msgs))
	for _, m := range msgs {
		if t, ok := m.(*TextMessage); ok && t.Text == EscalationNoticeText {
			continue
		}
		out = append(out, m)
	}
	return out
}
