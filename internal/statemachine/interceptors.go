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

		// En el paso de confirmación de cita, un archivo/foto NO debe chocar con "no esperaba una
		// imagen": el paciente suele reenviar la orden creyendo que falta algo. Se deja pasar para que
		// el RetryPrompt de CONFIRM_BOOKING le recuerde que la cita AÚN no está agendada y debe tocar
		// "Confirmar cita" (igual en RECONFIRM_BOOKING, usado en reprogramación).
		if sess.CurrentState == StateConfirmBooking || sess.CurrentState == StateReconfirmBooking {
			return nil, false
		}

		// MAIN_MENU y FALLBACK_MENU: una foto/documento aquí = intención de agendar. Se deja pasar para
		// que PhotoIntentInterceptor (registrado después) arranque el flujo de agendar en vez del
		// dead-end. FALLBACK_MENU se alcanza tras un handoff fallido: el paciente suele reintentar
		// mandando su orden (auditoría) y no debe toparse un segundo callejón.
		if sess.CurrentState == StateMainMenu || sess.CurrentState == StateFallbackMenu {
			return nil, false
		}

		// OUT_OF_HOURS_MENU: fuera de horario NO se agenda (política). Una foto aquí = orden → en vez del
		// dead-end genérico, se acusa recibo y se recuerdan los horarios de agendamiento.
		if sess.CurrentState == StateOutOfHoursMenu {
			return NewResult(sess.CurrentState).
				WithText("Recibimos tu orden 📷. Nuestro horario para *agendar citas* es *Lunes a Viernes de 7:00 a.m. a 6:00 p.m.* y *Sábados de 7:00 a.m. a 12:00 m.*\n\nEscríbenos en ese horario y con gusto te ayudamos a programar tu cita. 😊").
				WithEvent("image_out_of_hours", map[string]interface{}{"state": sess.CurrentState}), true
		}

		// Primer mensaje de la sesión (CHECK_BUSINESS_HOURS): la orden llega como primer mensaje →
		// PhotoStartInterceptor la guarda (stash) y arranca el flujo normal. CONFIRM_OCR_RESULT: una
		// imagen aquí es una PÁGINA ADICIONAL de la orden → PhotoAppendInterceptor la fusiona.
		if sess.CurrentState == StateCheckBusinessHours || sess.CurrentState == StateConfirmOCRResult {
			return nil, false
		}

		// Una orden enviada mientras el bot pregunta OTRA cosa se GUARDA en vez de descartarse.
		// Auditoría 2026-07-27: 11 de 15 rechazos de imagen del día cayeron en ASK_CLIENT_TYPE, con
		// pacientes reenviando la misma foto hasta 6 veces antes de rendirse — y el propio
		// PhotoIntentInterceptor los manda a ese estado diciéndoles "no necesitas reenviarla". Al
		// guardarla, askMedicalOrderHandler la consume después (stashed_order_used) sin re-pedirla.
		//
		// GUARDA: solo si la orden AÚN no se ha leído (ocr_cups_json vacío). Después de leída, una foto
		// es otra cosa (p.ej. el examen de laboratorio en GFR_CREATININE) y tomarla por orden médica
		// sería peor que no guardarla. Tampoco se pisa un stash previo: vale el primero.
		mediaURL := ""
		switch msg.MessageType {
		case "image":
			mediaURL = msg.ImageURL
		case "document":
			mediaURL = msg.DocumentURL
		}
		orderAlreadyRead := sess.GetContext("ocr_cups_json") != ""
		canStash := mediaURL != "" && !orderAlreadyRead && sess.GetContext("stashed_order_url") == ""

		// Emitir también como flow_event para que el rechazo sea VISIBLE en el funnel de la skill
		// auditora (el chat_event por sí solo era un punto ciego: ni ERROR ni caída de funnel). El attr
		// `state` distingue el caso benigno (imagen suelta en un menú) del BUG (rechazo en un estado que
		// SÍ espera foto, p.ej. un UPLOAD_* nuevo sin whitelistear); `stashed` distingue el callejón real
		// (la foto se perdió) de la recuperación (la foto quedó guardada).
		observability.Emit(observability.TraceSession(sess.ID), "agendar", "image_out_of_context",
			observability.EmitOpts{Phone: sess.PhoneNumber, Attrs: map[string]interface{}{
				"state":   sess.CurrentState,
				"stashed": canStash,
			}})

		if canStash {
			return NewResult(sess.CurrentState).
				WithText("Recibí tu orden 📷 y la guardo para usarla apenas lleguemos a ese paso — no necesitas reenviarla.\n\nSigamos con lo que te acabo de preguntar. 👆").
				WithContext("stashed_order_url", mediaURL).
				WithEvent("image_stashed_out_of_context", map[string]interface{}{
					"state": sess.CurrentState,
				}), true
		}

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
