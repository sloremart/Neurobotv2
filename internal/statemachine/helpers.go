package statemachine

import (
	"fmt"
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

// ValidateWithRetry valida input del usuario con reintentos automáticos.
// Retorna nil si el input es válido (caller debe procesarlo).
// Retorna StateResult si hay error de validación o escalación por reintentos.
func ValidateWithRetry(sess *session.Session, input string, validate func(string) bool, errorMsg string) *StateResult {
	if validate(input) {
		sess.RetryCount = 0
		return nil
	}

	sess.RetryCount++

	if sess.RetryCount >= maxRetries {
		sess.RetryCount = 0
		// Escalate to agent first; if agent unavailable, escalation handler falls back to FALLBACK_MENU
		return NewResult(StateEscalateToAgent).
			WithText(EscalationNoticeText).
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
	sess.RetryCount++

	if sess.RetryCount >= maxRetries {
		sess.RetryCount = 0
		// Escalate to agent first; if agent unavailable, escalation handler falls back to FALLBACK_MENU
		return NewResult(StateEscalateToAgent).
			WithText(EscalationNoticeText).
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
