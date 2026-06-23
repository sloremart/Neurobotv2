package statemachine

import (
	"context"
	"strings"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/session"
)

// InputType indica qué tipo de input espera un handler.
type InputType int

const (
	InputText   InputType = iota // Texto validado con TextValidate
	InputButton                  // Postback de botón/lista (Options)
	InputImage                   // Mensaje tipo imagen
	InputAny                     // Handler maneja todo internamente
)

// contextKey tipo privado para context.Value (evita colisión con otros packages).
type contextKey string

const validatedPayloadKey contextKey = "validated_payload"

// HandlerConfig define validación declarativa para un estado interactivo.
type HandlerConfig struct {
	InputType    InputType                            // Qué tipo de input espera
	TextValidate func(string) bool                    // Para InputText: función de validación
	Options      []string                             // Para InputButton: payloads válidos
	ErrorMsg     string                               // Mensaje de error en retry
	RetryPrompt  func(*session.Session, *StateResult) // Rebuild UI en retry (opcional)
	Handler      StateHandler                         // Solo lógica de negocio (input ya validado)
}

// ValidatedPayload extrae el payload validado del context (para InputButton handlers).
func ValidatedPayload(ctx context.Context) string {
	v, _ := ctx.Value(validatedPayloadKey).(string)
	return v
}

// RegisterWithConfig registra un handler con validación declarativa.
// Internamente crea un wrapper que valida primero y luego llama al Handler de negocio.
func (m *Machine) RegisterWithConfig(state string, cfg HandlerConfig) {
	switch cfg.InputType {
	case InputButton:
		m.Register(state, wrapButtonHandler(cfg))
	case InputText:
		m.Register(state, wrapTextHandler(cfg))
	case InputImage:
		m.Register(state, wrapImageHandler(cfg))
	case InputAny:
		m.Register(state, cfg.Handler)
	default:
		m.Register(state, cfg.Handler)
	}
}

// wrapButtonHandler envuelve un handler de botones con validación automática.
func wrapButtonHandler(cfg HandlerConfig) StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*StateResult, error) {
		result, selected := ValidateButtonResponse(sess, msg, cfg.Options...)
		if result != nil {
			// Only show retry prompt for normal retries (same state), not when escalating
			if cfg.RetryPrompt != nil && result.NextState == sess.CurrentState {
				// Clear default text messages — RetryPrompt rebuilds the full UI in one message
				result.Messages = nil
				cfg.RetryPrompt(sess, result)
			}
			return result, nil
		}

		// Pasar payload validado por context.Value
		ctx = context.WithValue(ctx, validatedPayloadKey, selected)
		return cfg.Handler(ctx, sess, msg)
	}
}

// isReservedKeyword returns true for internal trigger texts (e.g. __resume__)
// that should never be treated as valid user input.
func IsReservedKeyword(s string) bool {
	return len(s) > 4 && strings.HasPrefix(s, "__") && strings.HasSuffix(s, "__")
}

// wrapTextHandler envuelve un handler de texto con validación automática.
func wrapTextHandler(cfg HandlerConfig) StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*StateResult, error) {
		input := strings.TrimSpace(msg.Text)

		errorMsg := cfg.ErrorMsg
		if errorMsg == "" {
			errorMsg = "Respuesta no válida. Intenta de nuevo."
		}

		// Reject internal trigger texts (e.g. __resume__) so they never
		// pass validation and get stored as real data.
		if IsReservedKeyword(input) {
			input = ""
		}

		validate := cfg.TextValidate
		if validate == nil {
			validate = func(string) bool { return true }
		}

		retryResult := ValidateWithRetry(sess, input, validate, errorMsg)
		if retryResult != nil {
			// Only show retry prompt for normal retries (same state), not when escalating
			if cfg.RetryPrompt != nil && retryResult.NextState == sess.CurrentState {
				// Clear default text messages — RetryPrompt rebuilds the full UI in one message
				retryResult.Messages = nil
				cfg.RetryPrompt(sess, retryResult)
			}
			return retryResult, nil
		}

		return cfg.Handler(ctx, sess, msg)
	}
}

// wrapImageHandler envuelve un handler de imagen con validación automática.
func wrapImageHandler(cfg HandlerConfig) StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*StateResult, error) {
		if msg.MessageType == "image" {
			sess.RetryCount = 0
			return cfg.Handler(ctx, sess, msg)
		}

		// No es imagen → retry
		errorMsg := cfg.ErrorMsg
		if errorMsg == "" {
			errorMsg = "Por favor envía una imagen."
		}

		result := RetryOrEscalate(sess, errorMsg)
		// Only show retry prompt for normal retries (same state), not when escalating
		if cfg.RetryPrompt != nil && result.NextState == sess.CurrentState {
			// Clear default text messages — RetryPrompt rebuilds the full UI in one message
			result.Messages = nil
			cfg.RetryPrompt(sess, result)
		}
		return result, nil
	}
}
