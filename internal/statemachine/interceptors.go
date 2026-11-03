package statemachine

import (
	"context"
	"fmt"
	"strings"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/observability"
	"github.com/neuro-bot/neuro-bot/internal/session"
)

// MenuResetInterceptor permite al usuario volver al inicio con keywords.
// Gracias al auto-chain en Process(), el estado GREETING (automático) se ejecuta
// de inmediato y el usuario ve el saludo + menú sin necesidad de enviar otro mensaje.
func MenuResetInterceptor() Interceptor {
	keywords := menuResetKeywords

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

// menuResetKeywords son las palabras que vuelven al inicio desde cualquier paso.
var menuResetKeywords = map[string]bool{
	"menu": true, "menú": true, "inicio": true, "reiniciar": true, "0": true,
}

// IgnoredByEscalationCap dice si la sesión está cortada por el tope de escalaciones
// (CtxEscalationCapped) y esta entrada no es una de las que se siguen atendiendo: en FALLBACK_MENU
// solo pasan las pulsaciones de los botones del corte y la palabra de reinicio. Lo consulta el
// worker antes de renovar la sesión (una entrada ignorada no la mantiene viva) y
// EscalationCapInterceptor dentro de la máquina.
func IgnoredByEscalationCap(sess *session.Session, msg bird.InboundMessage) bool {
	if sess.CurrentState != StateFallbackMenu || sess.GetContext(CtxEscalationCapped) == "" {
		return false
	}
	if msg.IsPostback {
		return msg.PostbackPayload != "action:restart" && msg.PostbackPayload != "action:end"
	}
	return !menuResetKeywords[strings.TrimSpace(strings.ToLower(msg.Text))]
}

// TraceEscalationCapIgnored deja la traza de una entrada ignorada por el tope.
func TraceEscalationCapIgnored(sess *session.Session, msg bird.InboundMessage) {
	observability.Emit(observability.TraceSession(sess.ID), "escalacion", "escalation_cap_input_ignored",
		observability.EmitOpts{Phone: sess.PhoneNumber, Reason: msg.MessageType})
}

// EscalationCapInterceptor deja en silencio la sesión cortada por el tope de escalaciones: lo que
// IgnoredByEscalationCap descarta no recibe respuesta (queda un evento), con la conducta de siempre
// para lo demás. Un contestador automático que responde a cada mensaje no recibe nada más hasta que
// tome la sesión un agente o venza. Va PRIMERO, antes de las palabras clave de escalación.
func EscalationCapInterceptor() Interceptor {
	return func(_ context.Context, sess *session.Session, msg bird.InboundMessage) (*StateResult, bool) {
		if !IgnoredByEscalationCap(sess, msg) {
			return nil, false
		}
		TraceEscalationCapIgnored(sess, msg)
		return NewResult(StateFallbackMenu).
			WithEvent("escalation_cap_input_ignored", map[string]interface{}{"type": msg.MessageType}), true
	}
}

// EscalationCapKeys son las claves del tope de escalaciones de la sesión. Las borra el retorno de
// un agente que atendió el caso (fue una persona, no un lazo del bot); un no-show o una cola
// vencida no las tocan.
func EscalationCapKeys() []string {
	return []string{CtxEscalationCount, CtxEscalationCapped}
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

// ImageOutOfContextInterceptor rechaza imágenes y documentos fuera del estado UPLOAD_MEDICAL_ORDER.
// Con el horario de siempre; RegisterInterceptors cablea el vigente (imageOutOfContextInterceptor).
func ImageOutOfContextInterceptor() Interceptor {
	return imageOutOfContextInterceptor(nil)
}

// imageOutOfContextInterceptor es ImageOutOfContextInterceptor preguntando el horario a `m` en
// cada aviso (Machine.SetBusinessHoursText). m nil = el horario de siempre.
func imageOutOfContextInterceptor(m *Machine) Interceptor {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*StateResult, bool) {
		if msg.MessageType != "image" && msg.MessageType != "document" {
			return nil, false
		}

		// Imagen/documento SÍ es esperada en los estados de subida de orden: la 1ª orden
		// (UPLOAD_MEDICAL_ORDER) y la 2ª orden EMG de la consolidación de Fisiatría
		// (UPLOAD_EMG_ORDER). Sin este último, el interceptor rechazaba la foto de la 2ª orden
		// ANTES de llegar a uploadEmgOrderHandler y el paciente quedaba atascado (evento
		// image_out_of_context{state:UPLOAD_EMG_ORDER} en bucle).
		if sess.CurrentState == StateUploadMedicalOrder || sess.CurrentState == StateUploadEmgOrder ||
			sess.CurrentState == StateMedicationReceiveHistoria || sess.CurrentState == StateMedicationReceiveOrden {
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
				WithText(outOfHoursOrderText(m.businessHoursTextValue())).
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
		// sería peor que no guardarla. Las hojas siguientes se ACUMULAN (una orden de varias páginas se
		// manda como varias fotos seguidas), hasta el tope de MaxStashedOrderPages.
		mediaURL := ""
		switch msg.MessageType {
		case "image":
			mediaURL = msg.ImageURL
		case "document":
			mediaURL = msg.DocumentURL
		}
		orderAlreadyRead := sess.GetContext("ocr_cups_json") != ""
		stashedPages := StashedOrderURLs(sess)
		stashFull := len(stashedPages) >= MaxStashedOrderPages
		canStash := mediaURL != "" && !orderAlreadyRead && !stashFull

		// stashReason explica POR QUÉ no se guardó. Sin él, `stashed=false` obliga a inferir la causa
		// desde fuera (auditoría ciclo 130: no se podía distinguir "ya había stash" de "no venía media").
		stashReason := ""
		switch {
		case mediaURL == "":
			stashReason = "no_media"
		case orderAlreadyRead:
			stashReason = "already_read"
		case stashFull:
			stashReason = "stash_full"
		}

		// Emitir también como flow_event para que el rechazo sea VISIBLE en el funnel de la skill
		// auditora (el chat_event por sí solo era un punto ciego: ni ERROR ni caída de funnel). El attr
		// `state` distingue el caso benigno (imagen suelta en un menú) del BUG (rechazo en un estado que
		// SÍ espera foto, p.ej. un UPLOAD_* nuevo sin whitelistear); `stashed` distingue el callejón real
		// (la foto se perdió) de la recuperación (la foto quedó guardada).
		pages := stashedPages
		if canStash {
			pages = AppendStashedOrderURL(stashedPages, mediaURL)
		}
		observability.Emit(observability.TraceSession(sess.ID), "agendar", "image_out_of_context",
			observability.EmitOpts{Phone: sess.PhoneNumber, Attrs: map[string]interface{}{
				"state":        sess.CurrentState,
				"stashed":      canStash,
				"stash_reason": stashReason,
				"n":            len(pages),
			}})

		if canStash {
			// La primera hoja se acusa como "tu orden"; las siguientes, como páginas adicionales. Decirle
			// "ya tengo tu orden" a quien acaba de mandar la hoja 2 es falso y lo deja creyendo que el bot
			// tiene algo que no tiene (auditoría ciclo 133).
			text := "Recibí tu orden 📷 y la guardo para usarla apenas lleguemos a ese paso — no necesitas reenviarla.\n\nSigamos con lo que te acabo de preguntar. 👆"
			event := "image_stashed_out_of_context"
			if len(stashedPages) > 0 {
				text = fmt.Sprintf("Recibí también esa hoja de tu orden 📄 (llevo %d). Las uso todas apenas lleguemos a ese paso.\n\nSigamos con lo que te acabo de preguntar. 👆", len(pages))
				event = "image_stash_page_added"
			}
			return NewResult(sess.CurrentState).
				WithText(text).
				WithContext(StashedOrderURLsKey, EncodeStashedOrderURLs(pages)).
				WithClearCtx(legacyStashedOrderURLKey).
				WithEvent(event, map[string]interface{}{
					"state": sess.CurrentState,
					"pages": len(pages),
				}), true
		}

		// Tope de páginas alcanzado: el paciente sigue mandando hojas. No es un callejón (sigue dentro del
		// flujo de agendar) y decirle "primero selecciona la opción de agendar cita" sería falso — eso lo
		// empujaba a reintentar o a pedir un agente (auditoría ciclo 130, H130-2).
		if stashFull && !orderAlreadyRead && mediaURL != "" {
			return NewResult(sess.CurrentState).
				WithText(fmt.Sprintf("Ya tengo %d páginas de tu orden 📷, no necesitas reenviarlas.\n\nSigamos con lo que te acabo de preguntar. 👆", len(stashedPages))).
				WithEvent("image_stash_full", map[string]interface{}{
					"state": sess.CurrentState,
					"pages": len(stashedPages),
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

// escalationKeywords son las palabras con las que el paciente pide un humano desde cualquier estado
// (R-CHAT-04). Compartidas con IsEscalationKeyword para que la capa de recuperación IA reconozca el
// mismo escape que el interceptor y no intente "recuperar" un pedido explícito de agente.
var escalationKeywords = map[string]bool{
	"agente": true, "asesor": true, "humano": true, "ayuda": true,
	"asesora": true, "asesores": true, "agentes": true,
}

// IsEscalationKeyword indica si el texto es un pedido explícito de hablar con un humano.
func IsEscalationKeyword(text string) bool {
	return escalationKeywords[strings.TrimSpace(strings.ToLower(text))]
}

// EscalationKeywordsInterceptor allows users to request a human agent from any state
// by typing keywords like "agente", "asesor", "humano", "ayuda" (R-CHAT-04).
func EscalationKeywordsInterceptor() Interceptor {
	keywords := escalationKeywords

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

		// Coincidencia exacta (palabra sola) o como token dentro de una frase.
		// "agente" → sí. "quiero hablar con un agente" → sí. "agentecia" → no.
		matched := keywords[input]
		if !matched {
			for _, word := range strings.Fields(input) {
				// Quitar puntuación final común (,.:!?)
				word = strings.TrimRight(word, ".,;:!?")
				if keywords[word] {
					matched = true
					break
				}
			}
		}
		if !matched {
			return nil, false
		}

		sess.RetryCount = 0
		// La marca dice que el PACIENTE pidió la persona: el handler de escalación la usa para
		// el `reason` del sobre y para decidir si avisarle en Néctar.
		return NewResult(StateEscalateToAgent).
			WithText(EscalationNoticeText).
			WithContext(CtxPatientAskedForAgent, "1").
			WithContext(CtxEscalationReason, EscalationReasonPatientAsked).
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

// outOfHoursOrderText es el acuse de la orden que llega fuera de horario, con el horario vigente
// en negrilla. Con el horario de siempre es, letra por letra, el texto de siempre; cerrado toda
// la semana (hours "") no hay horario que nombrar.
func outOfHoursOrderText(hours string) string {
	if hours == "" {
		return "Recibimos tu orden 📷. Escríbenos más tarde y con gusto te ayudamos a programar tu cita. 😊"
	}
	return "Recibimos tu orden 📷. Nuestro horario para *agendar citas* es " + hours +
		"\n\nEscríbenos en ese horario y con gusto te ayudamos a programar tu cita. 😊"
}

// RegisterInterceptors registra todos los interceptores en la máquina
func RegisterInterceptors(machine *Machine) {
	machine.AddInterceptor(EscalationCapInterceptor()) // First: la sesión cortada no responde a nada más
	machine.AddInterceptor(UnsupportedMessageInterceptor())
	machine.AddInterceptor(imageOutOfContextInterceptor(machine))
	machine.AddInterceptor(EscalationKeywordsInterceptor()) // Before menu reset
	machine.AddInterceptor(MenuResetInterceptor())
	machine.AddInterceptor(AIRecoveryInterceptor(machine)) // Last: keywords/menu escape first
}
