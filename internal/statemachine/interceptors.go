package statemachine

import (
	"context"
	"strings"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/observability"
	"github.com/neuro-bot/neuro-bot/internal/session"
)

// MenuResetInterceptor permite al usuario volver al inicio con keywords.
// Gracias al auto-chain en Process(), el estado GREETING (automático) se ejecuta
// de inmediato y el usuario ve el saludo + menú sin necesidad de enviar otro mensaje.
func MenuResetInterceptor() Interceptor {
	keywords := map[string]bool{
		"menu": true, "menú": true, "inicio": true, "reiniciar": true, "0": true,
	}

	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*StateResult, bool) {
		// No interceptar en estados terminales
		if sess.CurrentState == StateTerminated || sess.CurrentState == StateEscalated {
			return nil, false
		}

		// No interceptar postbacks (son respuestas a botones)
		if msg.IsPostback {
			return nil, false
		}

		input := strings.TrimSpace(strings.ToLower(msg.Text))
		if !keywords[input] {
			return nil, false
		}

		sess.RetryCount = 0

		result := NewResult(StateGreeting).
			WithEvent("menu_reset", map[string]interface{}{
				"from_state": sess.CurrentState,
				"keyword":    input,
			})

		// Señal especial para ClearAllContext
		result.ClearCtx = []string{"__all__"}

		return result, true
	}
}

// UnsupportedMessageInterceptor rechaza tipos de mensaje no soportados
func UnsupportedMessageInterceptor() Interceptor {
	unsupported := map[string]bool{
		"audio": true, "video": true, "location": true,
		"contact": true, "sticker": true,
	}

	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*StateResult, bool) {
		if !unsupported[msg.MessageType] {
			return nil, false
		}

		result := NewResult(sess.CurrentState).
			WithText("⚠️ Solo puedo procesar mensajes de texto y fotos de órdenes médicas. Por favor, envía tu respuesta como texto.").
			WithEvent("unsupported_message", map[string]interface{}{
				"type":  msg.MessageType,
				"state": sess.CurrentState,
			})

		return result, true
	}
}

// ImageOutOfContextInterceptor rechaza imágenes y documentos fuera del estado UPLOAD_MEDICAL_ORDER
func ImageOutOfContextInterceptor() Interceptor {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*StateResult, bool) {
		if msg.MessageType != "image" && msg.MessageType != "document" {
			return nil, false
		}

		// Imagen/documento SÍ es esperada en los estados de subida de orden: la 1ª orden
		// (UPLOAD_MEDICAL_ORDER) y la 2ª orden EMG de la consolidación de Fisiatría
		// (UPLOAD_EMG_ORDER). Sin este último, el interceptor rechazaba la foto de la 2ª orden
		// ANTES de llegar a uploadEmgOrderHandler y el paciente quedaba atascado (evento
		// image_out_of_context{state:UPLOAD_EMG_ORDER} en bucle).
		if sess.CurrentState == StateUploadMedicalOrder || sess.CurrentState == StateUploadEmgOrder {
			return nil, false
		}

		// Emitir también como flow_event para que el rechazo sea VISIBLE en el funnel de la skill
		// auditora (el chat_event por sí solo era un punto ciego: ni ERROR ni caída de funnel). El attr
		// `state` distingue el caso benigno (imagen suelta en un menú) del BUG (rechazo en un estado que
		// SÍ espera foto, p.ej. un UPLOAD_* nuevo sin whitelistear).
		observability.Emit(observability.TraceSession(sess.ID), "agendar", "image_out_of_context",
			observability.EmitOpts{Phone: sess.PhoneNumber, Attrs: map[string]interface{}{"state": sess.CurrentState}})

		result := NewResult(sess.CurrentState).
			WithText("No esperaba una imagen en este momento. Si necesitas enviar una orden médica, primero selecciona la opción de agendar cita.").
			WithEvent("image_out_of_context", map[string]interface{}{
				"state": sess.CurrentState,
			})

		return result, true
	}
}

// EscalationKeywordsInterceptor allows users to request a human agent from any state
// by typing keywords like "agente", "asesor", "humano", "ayuda" (R-CHAT-04).
func EscalationKeywordsInterceptor() Interceptor {
	keywords := map[string]bool{
		"agente": true, "asesor": true, "humano": true, "ayuda": true,
	}

	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*StateResult, bool) {
		// No interceptar en estados terminales o ya escalados
		if sess.CurrentState == StateTerminated || sess.CurrentState == StateEscalated ||
			sess.CurrentState == StateEscalateToAgent {
			return nil, false
		}

		// No interceptar postbacks
		if msg.IsPostback {
			return nil, false
		}

		input := strings.TrimSpace(strings.ToLower(msg.Text))
		if !keywords[input] {
			return nil, false
		}

		sess.RetryCount = 0
		return NewResult(StateEscalateToAgent).
			WithEvent("escalation_requested", map[string]interface{}{
				"from_state": sess.CurrentState,
				"keyword":    input,
			}), true
	}
}

// AIRecoveryInterceptor entrega el mensaje del paciente al coordinador de recuperación (validador
// puro → LLM) en vez del handler normal mientras la sesión esté en modo recuperación IA. Se registra
// DESPUÉS del de keywords de escalación y el de menú, para que "agente"/"menú" sigan como escape.
func AIRecoveryInterceptor(m *Machine) Interceptor {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*StateResult, bool) {
		rc := m.recovery
		if rc == nil || !rc.Active(sess) {
			return nil, false
		}
		// No interceptar postbacks (respuestas a botones); la recuperación es para texto libre.
		if msg.IsPostback {
			return nil, false
		}
		return rc.Handle(ctx, sess, msg)
	}
}

// RegisterInterceptors registra todos los interceptores en la máquina
func RegisterInterceptors(machine *Machine) {
	machine.AddInterceptor(UnsupportedMessageInterceptor())
	machine.AddInterceptor(ImageOutOfContextInterceptor())
	machine.AddInterceptor(EscalationKeywordsInterceptor()) // Before menu reset
	machine.AddInterceptor(MenuResetInterceptor())
	machine.AddInterceptor(AIRecoveryInterceptor(machine)) // Last: keywords/menu escape first
}
