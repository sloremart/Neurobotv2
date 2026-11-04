package handlers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/config"
	"github.com/neuro-bot/neuro-bot/internal/nectar"
	"github.com/neuro-bot/neuro-bot/internal/observability"
	"github.com/neuro-bot/neuro-bot/internal/recovery"
	"github.com/neuro-bot/neuro-bot/internal/session"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
	"github.com/neuro-bot/neuro-bot/internal/utils"
)

// escalationSessionCap: máximo de escalaciones por sesión (cortacircuito anti-bucle,
// incidente 11/12-ago-2026). Una conversación real rara vez necesita más de 3; a partir
// de ahí se asume lazo (recovery↔escalación, no-show↔re-escalación, etc.) y se corta.
const escalationSessionCap = 3

// bumpEscalationCount suma una vuelta al contador del tope y devuelve el valor nuevo, que el caller
// pone en el resultado: escrito solo en la sesión en memoria, se perdía al recargarla de la BD en el
// mensaje siguiente y el tope nunca saltaba entre mensajes.
func bumpEscalationCount(sess *session.Session) string {
	n, _ := strconv.Atoi(sess.GetContext(sm.CtxEscalationCount))
	v := strconv.Itoa(n + 1)
	sess.SetContext(sm.CtxEscalationCount, v)
	return v
}

// escalateMaxRetries: intentos totales para EscalateToAgent ante errores transitorios de la API.
// Delays: 0s → 1s → 2s entre intentos; peor caso ~3s extra antes de mostrar el fallback.
const escalateMaxRetries = 3

// escalationDisabledText es lo que oye el paciente cuando Néctar tiene apagado el escalado de su
// entidad (403 escalation_disabled): no se le promete un asesor ni se le dice que lo contactarán.
const escalationDisabledText = "En este momento no podemos comunicarte con un asesor por este medio. " +
	"¿Deseas volver al inicio o terminar el chat?"

// resumeSuppressedNectarText es la red de seguridad residual del corte anti-bucle del `__resume__`
// en Néctar: si aun así un retorno llega a escalar y el corte devuelve al paciente al menú, esta
// línea lo explica (allá no hay un aviso de no-show previo). Es residual: los menús de retorno no
// ofrecen reanudar tal cual en los pasos automáticos que re-escalarían, y los pasos con pregunta
// re-preguntan sin escalar.
const resumeSuppressedNectarText = "Sigamos por aquí 👍 Elige una opción del menú para continuar."

// EscalationCreator registra una fila por escalación (tabla escalations). Opcional (nil = no registra).
type EscalationCreator interface {
	Create(ctx context.Context, sessionID, phone, fromState, teamID, agentID, agentName string) error
}

// RegisterEscalationHandlers registra los handlers de escalación a agente (Fase 11).
func RegisterEscalationHandlers(m *sm.Machine, birdClient *bird.Client, cfg *config.Config, escRepo EscalationCreator) {
	m.Register(sm.StateEscalateToAgent, escalateHandler(m, birdClient, cfg, escRepo))
	m.Register(sm.StateEscalated, escalatedHandler())

	// BOT_DISABLED no es un estado: devolver «en el mismo paso» dejaría la sesión sin handler. La
	// única vuelta con sentido es empezar la atención desde el saludo.
	sm.RegisterStaticReturnMenu(sm.BotDisabledStep,
		sm.ReturnOption{
			Label: "Iniciar la atención del bot desde el saludo", Value: "bot_iniciar",
			Target: sm.StateGreeting, NoInput: true,
		})
	sm.HidePlainResume(sm.BotDisabledStep)
}

// ESCALATE_TO_AGENT (automático) — transfiere la conversación a un agente humano.
// Routes to the correct Bird team based on the CUPS procedure code. En Néctar el equipo se
// SUGIERE por grupo de CUPS en el sobre (escalation_nectar.go).
//
// Todas las salidas pasan por escalationExit.
func escalateHandler(m *sm.Machine, birdClient *bird.Client, cfg *config.Config, escRepo EscalationCreator) sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		// 1. Determine team based on service (CUPS code)
		cupsCode := sess.GetContext("cups_code")
		teamID := cfg.ResolveTeamForCups(cupsCode)

		// 2. Read actual pre-escalation state (set by machine.go auto-chain)
		preState := sess.GetContext(sm.CtxPreAutoState)
		if preState == "" || preState == sm.StateEscalateToAgent {
			preState = sess.CurrentState
		}
		sess.SetContext("pre_escalation_state", preState)

		nectarSession := session.IsNectarSession(msg.Phone)
		// askedForAgent: el PACIENTE pidió la persona (palabra clave o «agente» en el menú
		// post-acción). Decide el `reason` del sobre y, en Néctar, si se le avisa.
		askedForAgent := sess.GetContext(sm.CtxPatientAskedForAgent) == "1"
		// Al tope anti-bucle solo suman los ciclos del PACIENTE: pidió la persona o agotó los
		// reintentos (sm.EscalationCountsTowardCap). Las que dispara el bot (una orden dudosa, una
		// regla, un fallo) ni suman ni cortan, entregadas o desviadas. El kill switch
		// (BOT_ENABLED=false) tampoco, aunque el paciente haya pedido un asesor: escala cada mensaje
		// para mandarlo todo a los asesores.
		detail := sess.GetContext(sm.CtxEscalationReason)
		countsTowardCap := detail != sm.EscalationReasonBotDisabled &&
			(askedForAgent || sm.EscalationCountsTowardCap(detail))

		// COLA DEL PRÓXIMO DÍA HÁBIL, SOLO EN NÉCTAR. En los últimos minutos antes del cierre
		// (BUSINESS_HOURS_CLOSE_MARGIN_MIN) y con el horario cerrado, lo que se escale ya no le
		// cabe en el día a una persona: en vez de devolver al paciente al menú, el caso se encola
		// igual en Néctar y se le dice que lo atienden el próximo día hábil. Salvo que abrir quede
		// LEJOS (BUSINESS_HOURS_QUEUE_MAX_WAIT_MIN): la ventana de 24 h de WhatsApp se cerraría
		// mientras espera, y ahí vuelve el desvío de siempre.
		//
		// Bird queda como hoy: su aviso («te voy a conectar… un momento») viaja en el resultado
		// del caller y no puede cambiarse aquí, y su no-show devolvería el caso al bot; encolarlo
		// le prometería al paciente una atención inmediata que no llega hasta el día siguiente.
		var queue escalationQueue
		if nectarSession {
			queue = nextBusinessDayQueue(cfg)
		}
		queuedNextDay := queue.queue

		// CORTACIRCUITO ANTI-BUCLE (incidente 11/12-ago-2026: 18 pacientes con 1 WhatsApp/min
		// toda la noche, ~10.800 envíos). El no-show devolvía la sesión a un estado AUTOMÁTICO
		// determinista (VALIDATE_OCR→cups_none, bloqueo SANITAS…) que re-escalaba en el mismo
		// Process, y el checker re-disparaba el no-show al minuto. Dos guardas, ANTES de la capa
		// de recuperación y del gate de agentes (ninguna vía puede saltárselas):
		//
		// (1) Un __resume__ virtual (retorno de no-show o resume de agente, pool.go) NUNCA
		//     re-escala: no es un paciente pidiendo asesor, es el propio bot volviendo. Sin input
		//     nuevo, un estado automático produce la MISMA escalación → lazo cerrado. Se corta
		//     aquí y la sesión queda en menú esperando al paciente (la inactividad la cierra sola).
		if msg.Text == "__resume__" {
			observability.Emit(observability.TraceSession(sess.ID), "escalacion", "escalation_suppressed_resume",
				observability.EmitOpts{Phone: sess.PhoneNumber, Reason: preState})
			slog.Info("escalation suppressed (virtual __resume__, anti-loop)",
				"session_id", sess.ID, "phone", utils.MaskPhone(sess.PhoneNumber), "pre_state", preState)
			// AHORRO: solo el menú (1 mensaje cobrado, no 2). El aviso "No pudimos conectarte…"
			// ya salió en handleAgentNoShow justo antes de este __resume__; repetirlo con otro
			// texto era pagar un mensaje redundante en cada no-show (~decenas/día).
			// En Néctar no hubo aviso de no-show: sin una línea que lo explique, el paciente recibiría
			// el menú de golpe (P1-5(d)). Bird queda como hoy: el menú solo.
			r := sm.NewResult(sm.StateMainMenu).
				WithEvent("escalation_suppressed_resume", map[string]interface{}{"pre_state": preState})
			if nectarSession {
				r.Messages = append(r.Messages, &sm.TextMessage{Text: resumeSuppressedNectarText})
			}
			r.Messages = append(r.Messages, buildMainMenuList())
			return escalationDiverted(preState, r), nil
		}
		//
		// (2) Tope duro por sesión: a la (escalationSessionCap+1)-ésima escalación de un ciclo del
		//     paciente (countsTowardCap: keyword, reintentos agotados, recovery agotada, o un motivo
		//     sin clasificar) en una MISMA sesión se corta. Un mecanismo futuro sin motivo propio queda
		//     acotado a escalationSessionCap avisos por sesión. Las que dispara el bot no lo necesitan:
		//     cada una exige una entrada nueva del paciente (el __resume__ ya se cortó en (1)).
		//     EXENTO mientras la sesión tenga una escalación ENCOLADA para el próximo día hábil y
		//     su espera siga abierta (queuedWaitStillOpen): ese caso ya está en la cola de Néctar
		//     y desviarlo al menú de recuperación lo borraría de ella. La exención caduca con la
		//     apertura.
		//     EXENTA también la re-escalación por un dato del agente que no sirvió
		//     (CtxAgentInputRejection): la causó una persona, no un lazo del bot, y cada vuelta
		//     necesita otra acción humana; cortarla dejaría al paciente sin asesor.
		if n, _ := strconv.Atoi(sess.GetContext(sm.CtxEscalationCount)); countsTowardCap && n >= escalationSessionCap &&
			!queuedWaitStillOpen(cfg, sess) && sess.GetContext(sm.CtxAgentInputRejection) == "" {
			observability.Emit(observability.TraceSession(sess.ID), "escalacion", "escalation_cap_reached",
				observability.EmitOpts{
					Phone: sess.PhoneNumber, Reason: preState,
					Attrs: map[string]interface{}{"count": n},
				})
			slog.Warn("escalation suppressed (session cap reached)",
				"session_id", sess.ID, "phone", utils.MaskPhone(sess.PhoneNumber), "count", n, "pre_state", preState)
			// Desde el corte el bot deja de responder a la sesión hasta que la tome un agente o venza:
			// la marca se persiste y la lee EscalationCapInterceptor en FALLBACK_MENU.
			return escalationDiverted(preState, sm.NewResult(sm.StateFallbackMenu).
				WithContext(sm.CtxEscalationCapped, "1").
				// El corte no crea escalación: el texto no promete que alguien revisará el caso.
				WithButtons(
					"En este momento no podemos comunicarte con un asesor. Puedes volver al inicio o escribirnos más tarde.",
					sm.Button{Text: "Volver al inicio", Payload: "action:restart"},
					sm.Button{Text: "Terminar chat", Payload: "action:end"},
				).
				WithEvent("escalation_cap_reached", map[string]interface{}{"pre_state": preState, "count": n})), nil
		}

		// Capa de recuperación IA (§2.1): antes de escalar de verdad, darle la oportunidad de
		// desbloquear el input. Solo si el estado es opt-in y NO estamos ya en recuperación (si ya
		// lo estamos, llegamos aquí por presupuesto agotado o keyword del paciente → escalar).
		// NO se recupera con IA lo que rompió una persona: si llegamos aquí porque el dato que
		// devolvió un agente no se pudo usar, quien tiene que corregirlo es un agente. Se mira el
		// rechazo de ESTA vuelta (CtxAgentInputRejection, que escalationExit borra en cada salida)
		// y no el contador de la sesión, que no se borra y apagaría la IA para siempre.
		if rc := m.Recovery(); rc != nil && !rc.Active(sess) &&
			sess.GetContext(sm.CtxAgentInputRejection) == "" {
			if res, ok := rc.TryStart(ctx, sess, msg, preState); ok {
				return escalationExit(res), nil
			}
		}

		// Gate de disponibilidad: si NO hay agentes disponibles O el centro está CERRADO, NO crear
		// una escalación destinada a no-show/expiración. Se avisa el horario y se vuelve al menú (no
		// se pierde al paciente). Cubre todas las vías de escalación (keyword, reintentos agotados,
		// etc.) en un solo punto. Mismo criterio que POST_ACTION_MENU (auditoría: escalaciones
		// nocturnas al vacío → agent_no_show=275, escalation_expired=534/7d).
		// El chequeo de HORARIO existe porque HasAvailableAgents confía en la presencia de Bird, y
		// un agente que deja su estado encendido al irse mantiene el gate abierto toda la noche —
		// así pasó el bucle del 11/12-ago-2026 a las 4 a.m. El reloj del centro no depende de nadie.
		// En Néctar la disponibilidad es por conversación (HasAvailableAgentsFor); en Bird cae en
		// la presencia global de siempre.
		gate := ""
		//nolint:contextcheck // el cliente de mensajería no toma ctx (patrón existente: HasAvailableAgents)
		if birdClient != nil && !birdClient.HasAvailableAgentsFor(msg.Phone, sess.ConversationID) {
			gate = "no_agents"
		} else if !businessHoursOpen(cfg) {
			gate = gateOutOfHours
		}
		// LA APERTURA LEJANA desvía por sí sola: puede haber asesores, pero el que tome el caso ya
		// no podrá contestarle al paciente dentro de la ventana de 24 h. Se rotula out_of_hours
		// porque decide el horario, y el motivo lo separa del gate de disponibilidad.
		gateReason := ""
		if queue.farOpen() {
			gate = gateOutOfHours
			gateReason = queueReasonFarOpen
		}
		// En la ventana del próximo día hábil el gate NO desvía: «fuera de horario» o «no hay
		// asesores» dicen que el caso no se puede atender AHORA, que es justo lo que la cola
		// resuelve. Fuera de ella, un «no hay asesores» en plena jornada sigue desviando al menú.
		// La re-escalación por un dato del agente que no sirvió (CtxAgentInputRejection) tampoco
		// se desvía: la causó una persona y desviarla al menú dejaría al paciente sin asesor (igual
		// que se exime del tope y de la IA).
		if gate != "" && !queuedNextDay && sess.GetContext(sm.CtxAgentInputRejection) == "" {
			attrs := map[string]interface{}{"gate": gate}
			if gateReason != "" {
				attrs["motivo"] = gateReason
			}
			observability.Emit(observability.TraceSession(sess.ID), "escalacion", "escalation_no_agents",
				observability.EmitOpts{
					Phone: sess.PhoneNumber, Reason: preState,
					Attrs: attrs,
				})
			// El desvío también suma al tope anti-bucle (guard (2)): sin asesores, un contestador
			// automático que re-escala sin fin recibía este aviso y el menú en cada vuelta. Al paciente
			// que pide un asesor una o dos veces de noche le sigue llegando igual.
			r := sm.NewResult(sm.StateMainMenu).
				WithText(noAgentsText(cfg)).
				WithEvent("escalation_no_agents", map[string]interface{}{"pre_state": preState, "gate": gate})
			if countsTowardCap {
				r.WithContext(sm.CtxEscalationCount, bumpEscalationCount(sess))
			}
			r.Messages = append(r.Messages, buildMainMenuList())
			return escalationDiverted(preState, r), nil
		}

		slog.Debug(
			"escalation_start",
			"session_id", sess.ID,
			"phone", utils.MaskPhone(msg.Phone),
			"cups_code", cupsCode,
			"team_id", teamID,
			"state", preState,
		)
		sess.SetContext("escalation_team", teamID)

		// 3. Resolve conversationID: msg → session → cache (from conversation.created webhook)
		conversationID := msg.ConversationID
		if conversationID == "" {
			conversationID = sess.ConversationID
		}
		if conversationID == "" {
			conversationID = birdClient.GetCachedConversationID(msg.Phone)
			if conversationID != "" {
				sess.ConversationID = conversationID
			}
		}

		// Resolve team name for display
		teamName := resolveTeamName(teamID, cfg)

		// Diagnóstico por-capa de la resolución del conversation_id (ciclo 98). Sin esto, el fallo
		// "empty conversation ID" era una caja negra (attrs:null) y se cerraba "por ausencia". Todo
		// booleano/enum, sin PII; se adjunta al evento y al log ERROR si el handoff falla, para saber
		// EXACTAMENTE qué capa cayó en la próxima ocurrencia (webhook/caché/send/fetch/lookup).
		diagMsgConvID := msg.ConversationID != "" // capa 1: ¿el inbound traía convId?
		diagCacheHit := false                     // capa 3/4a: ¿la caché lo tenía en algún momento?
		diagSentOK := false                       // capa 4: ¿el pre-envío devolvió messageID?
		diagFetchOK := false                      // capa 4b: ¿el GET del mensaje resolvió?
		diagLookup := "skipped"                   // capa 4c/4d: found | not_found | error | skipped | created

		// 4. If conversationID is still empty, send patient message first.
		// The Channels API response contains conversationId which gets cached.
		if conversationID == "" {
			slog.Warn(
				"escalation_no_conversation_id",
				"session_id", sess.ID,
				"phone", utils.MaskPhone(msg.Phone),
				"msg_conv_id", msg.ConversationID,
				"sess_conv_id", sess.ConversationID,
			)
			//nolint:contextcheck // SendText no toma ctx (patrón existente del pre-envío)
			sentMsgID, sendErr := birdClient.SendText(msg.Phone, "", sm.EscalationNoticeText)
			diagSentOK = sentMsgID != ""
			// En Néctar el aviso que sale aquí es el mismo que el paso 4b mandaría: se marca para
			// que no se repita. Bird no se toca (su aviso viaja en el resultado del caller).
			if nectarSession && sendErr == nil {
				sess.SetContext(sm.CtxEscalationNoticeFlushed, "1")
			}
			if sendErr != nil {
				slog.Error(
					"escalation_pre_send_failed",
					"phone", utils.MaskPhone(msg.Phone),
					"session_id", sess.ID,
					"error", sendErr,
				)
			}
			// Check cache — el webhook de conversación de Bird puede haberla poblado.
			conversationID = birdClient.GetCachedConversationID(msg.Phone)
			if conversationID != "" {
				diagCacheHit = true
			}
			// Vía FIABLE: resolver el conversationId directamente del mensaje recién enviado (GET del
			// mensaje trae conversationId). Es más fiable que el lookup por lista (createdAt paginado),
			// que fallaba para pacientes recurrentes cuyo hilo queda fuera de las páginas → "empty
			// conversation ID". Esta es la causa raíz del incidente abierto en auditoría.
			if conversationID == "" && sentMsgID != "" {
				if resolved := birdClient.FetchMessageConversationID(ctx, sentMsgID); resolved != "" {
					conversationID = resolved
					diagFetchOK = true
					birdClient.CacheConversationID(msg.Phone, resolved)
					slog.Info("escalation_conv_id_from_message",
						"phone", utils.MaskPhone(msg.Phone), "conversation_id", resolved)
				}
			}
			// Último recurso: lookup por lista.
			if conversationID == "" && sendErr == nil {
				if looked, lookErr := birdClient.LookupConversationByPhone(msg.Phone); lookErr != nil {
					diagLookup = "error"
				} else if looked != "" {
					conversationID = looked
					diagLookup = "found"
				} else {
					diagLookup = "not_found"
				}
			}
			// Capa 4d — último recurso ABSOLUTO (auditoría 2026-07-23): crear la conversación
			// explícitamente (schema validado en vivo contra Bird). Los casos residuales son contactos
			// recurrentes cuyo hilo no aparece en el lookup (10 páginas) y sin webhooks; el mensaje SÍ
			// le llegó al paciente (sent_ok) pero el handoff caía a PICKUP MANUAL por no tener canal.
			if conversationID == "" && sendErr == nil {
				if created, cerr := birdClient.CreateConversationForPhone(ctx, msg.Phone); cerr != nil {
					slog.Warn(
						"escalation_create_conversation_failed",
						"phone", utils.MaskPhone(msg.Phone),
						"session_id", sess.ID,
						"error", cerr,
					)
				} else if created != "" {
					conversationID = created
					diagLookup = "created"
				}
			}
			if conversationID != "" {
				sess.ConversationID = conversationID
			}
		}

		// 4b. EL AVISO AL PACIENTE DE NÉCTAR VA ANTES DEL ESCALATE, Y ES UNO SOLO. Después del
		// escalate la conversación ya no es del bot y todo envío muere en 404, así que no puede
		// viajar en el resultado de este turno. En Bird no se toca: el aviso sigue viajando en el
		// resultado del caller, como siempre.
		//
		// Se calla en dos casos fuera de la cola: si el pre-envío ya sacó el aviso del caller
		// (CtxEscalationNoticeFlushed), y si el paciente NO pidió la persona — el bot se atascó y,
		// para él, el flujo continúa cuando el agente devuelva el dato; anunciarle un agente
		// describe mal lo que va a pasar y cuesta un mensaje. En la cola NO se calla nunca: lo único
		// que el paciente necesita saber a las 18:30 es que la espera es hasta el próximo día hábil.
		if nectarSession {
			notice := escalationNoticeText(cfg, queuedNextDay)
			if sess.GetContext(sm.CtxEscalationNoticeFlushed) == "1" && !queuedNextDay {
				notice = ""
			}
			if !askedForAgent && !queuedNextDay {
				notice = ""
			}
			// Los callers sin texto propio (particular liga, bot desactivado) no avisarían a nadie:
			// el aviso estándar sale igual, salvo que el pre-envío ya lo haya sacado.
			if !queuedNextDay && sess.GetContext(sm.CtxEscalationNoticeFlushed) != "1" &&
				reasonNotifiesPatient(sess.GetContext(sm.CtxEscalationReason)) {
				notice = sm.EscalationNoticeText
			}
			if notice != "" {
				//nolint:contextcheck // SendText no toma ctx (patrón existente del pre-envío del paso 4)
				if _, serr := birdClient.SendText(msg.Phone, conversationID, notice); serr != nil {
					slog.Warn("escalation: no se pudo avisar al paciente antes de escalar",
						"error", serr, "session_id", sess.ID)
				}
			}
		}

		// 5. Try to escalate — reintentos silenciosos ante errores transitorios de la API.
		// El resumen del caso se arma ANTES y viaja con el escalate: en Néctar es el único sitio
		// donde cabe; en Bird sale como nota interna justo después de asignar, como siempre. El
		// texto y los grupos (`facts`) salen de la MISMA construcción (summary_groups.go).
		// Delays: 0s → 1s → 2s entre intentos (≤3s total de espera extra).
		summary, facts := summaryForAgent(sess, cupsCode, teamName)
		// Motivo del vocabulario cerrado de Néctar (Bird lo ignora), de la misma marca que decide
		// el aviso.
		reason := nectar.ReasonBotCannotAnswer
		if askedForAgent {
			reason = nectar.ReasonPatientAskedForHuman
		}
		// Candado del botón «Resolver»: lo declara el paso que se atascó, igual que su menú.
		resolveLock, _ := sm.ResolveGuardFor(preState)
		envelope := escalationEnvelope(cfg, m, sess, preState, reason, summary, facts, resolveLock)
		var assignedAgentID, assignedAgentName string
		var err error
		for attempt := 0; attempt < escalateMaxRetries; attempt++ {
			if attempt > 0 {
				slog.Warn("escalation_retry",
					"attempt", attempt, "prev_error", err,
					"phone", utils.MaskPhone(msg.Phone), "session_id", sess.ID)
				time.Sleep(time.Duration(attempt) * time.Second)
			}
			assignedAgentID, assignedAgentName, err = birdClient.EscalateToAgentWithSummary(ctx, conversationID, msg.Phone,
				teamID, teamName, sess.PatientName, cfg.BirdTeamFallback, envelope)
			if err == nil || errors.Is(err, nectar.ErrEscalationDisabled) {
				break
			}
		}
		if errors.Is(err, nectar.ErrEscalationDisabled) {
			// El escalado está apagado para la entidad: no es un fallo de canal ni se reintenta, y
			// el paciente no recibe la promesa de un asesor que no va a venir.
			observability.Emit(observability.TraceSession(sess.ID), "escalacion", "escalation_disabled",
				observability.EmitOpts{Phone: sess.PhoneNumber, Reason: preState})
			slog.Warn("escalation: escalado apagado para la entidad en Néctar",
				"session_id", sess.ID, "phone", utils.MaskPhone(msg.Phone), "pre_state", preState)
			return escalationDiverted(preState, sm.NewResult(sm.StateFallbackMenu).
				WithClearCtx(recovery.CtxRecoveryActive, recovery.CtxRecoveryAttempts).
				WithButtons(escalationDisabledText,
					sm.Button{Text: "Volver al inicio", Payload: "action:restart"},
					sm.Button{Text: "Terminar chat", Payload: "action:end"},
				).
				WithEvent("escalation_disabled", map[string]interface{}{"pre_state": preState})), nil
		}
		if err != nil {
			// Diagnóstico por-capa (ciclo 98) + directiva de PICKUP MANUAL. El log ERROR va a Telegram
			// (AlertHandler) con el trace_id: sin canal Bird para el handoff, el agente NO ve el chat en
			// el Inbox, así que ops debe ubicar al paciente por session_id/trace en el dashboard y
			// contactarlo. Los flags dicen qué capa cayó: msg_conv_id/cache_hit=false sostenido ⇒ gap de
			// webhooks; lookup=error ⇒ API de Bird cae; lookup=not_found ⇒ el hilo no está en la lista.
			slog.Error(
				"escalation failed",
				"error", err,
				"phone", utils.MaskPhone(msg.Phone),
				"team_id", teamID,
				"conversation_id", conversationID,
				"session_id", sess.ID,
				"trace_id", observability.TraceSession(sess.ID),
				"msg_conv_id", diagMsgConvID,
				"cache_hit", diagCacheHit,
				"sent_ok", diagSentOK,
				"fetch_ok", diagFetchOK,
				"lookup", diagLookup,
				"action_required", "PICKUP MANUAL: sin canal Bird; ubicar al paciente por session_id/trace en el dashboard y contactarlo",
			)
			// Terminal medible del residual: no se pudo resolver/crear la conversación en Bird para el
			// handoff (p.ej. recurrente cuyo hilo no aparece en el lookup y el webhook no llegó a tiempo).
			// Sin esto, el fallo quedaba solo en logs; el funnel lo necesita para medir el residual real.
			// Se adjunta el diagnóstico por-capa para que la próxima ocurrencia sea demostrable (attrs).
			observability.Emit(observability.TraceSession(sess.ID), "escalacion", "escalation_no_channel",
				observability.EmitOpts{
					Phone:  msg.Phone,
					Reason: err.Error(),
					Attrs: map[string]interface{}{
						"msg_conv_id": diagMsgConvID,
						"cache_hit":   diagCacheHit,
						"sent_ok":     diagSentOK,
						"fetch_ok":    diagFetchOK,
						"lookup":      diagLookup,
					},
				})
			// B — No abandonar al paciente: el aviso "te voy a conectar…" viaja en los mensajes del caller, así
			// que en vez de un menú frío de reinicio se le confirma que un asesor lo contactará (el
			// pickup manual queda disparado por la alerta ERROR de arriba) y se le deja una salida.
			return escalationDiverted(preState, sm.NewResult(sm.StateFallbackMenu).
				WithClearCtx(recovery.CtxRecoveryActive, recovery.CtxRecoveryAttempts).
				WithButtons(
					"Estamos teniendo un inconveniente para conectarte con un asesor en este momento. "+
						"Un miembro de nuestro equipo te contactará a la brevedad. Mientras tanto, ¿deseas volver al inicio o terminar el chat?",
					sm.Button{Text: "Volver al inicio", Payload: "action:restart"},
					sm.Button{Text: "Terminar chat", Payload: "action:end"},
				).
				WithEvent("escalation_failed", map[string]interface{}{"error": err.Error()})), nil
		}

		// 5b. Persist conversationID in session
		if sess.ConversationID == "" {
			if cached := birdClient.GetCachedConversationID(msg.Phone); cached != "" {
				sess.ConversationID = cached
				conversationID = cached
			}
		}

		// 6. En Bird, el aviso al paciente YA viene en el StateResult del caller que escaló (texto
		// contextual propio, o EscalationNoticeText en los helpers/interceptor/post-action/recovery).
		// El envío directo que iba aquí duplicaba el aviso (~700 msgs/sem cobrados de más). El
		// pre-envío del paso 4 se conserva solo para el caso SIN conversationID. En Néctar el aviso
		// salió en el paso 4b.

		// 7. (el resumen para el agente ya viajó con el escalate, en el paso 5)

		// 8. Send contextual commands for the agent (Inbox only — invisible to patient). Néctar no
		// tiene nota interna: lo que el agente puede devolver viaja en el sobre (menú o descriptor).
		if !nectarSession {
			sendAgentCommandsNote(birdClient, msg.Phone, conversationID, buildAgentCommands(sess, cupsCode))
		}

		// 8. Mark session as escalated (in-memory, persisted by worker pool).
		// Sellar escalated_at / escalated_team aquí: el flujo real persiste vía Save() y, sin esto,
		// esas columnas quedaban NULL (el método manager.Escalate solo se usaba en tests).
		now := time.Now()
		sess.Status = session.StatusEscalated
		sess.EscalatedAt = &now
		sess.EscalatedTeam = teamID
		sess.AgentID = assignedAgentID
		sess.AgentName = assignedAgentName
		// Registrar la escalación (una fila por escalación, con el paso del chat donde se escaló).
		if escRepo != nil {
			if err := escRepo.Create(ctx, sess.ID, msg.Phone, preState, teamID, assignedAgentID, assignedAgentName); err != nil {
				slog.Warn("create escalation record failed", "session_id", sess.ID, "error", err)
			}
		}
		observability.Emit(observability.TraceSession(sess.ID), "escalacion", "escalated",
			observability.EmitOpts{Phone: sess.PhoneNumber, Reason: sess.GetContext(sm.CtxEscalationReason)})
		// El equipo sugerido es un campo del sobre de Néctar: Bird lo descarta, así que anotarlo en
		// una escalación de Bird contaría una sugerencia que nadie recibió.
		if nectarSession {
			emitSuggestedTeam(cfg, sess, envelope.SuggestedTeamID)
		}
		// El caso se encoló para el próximo día hábil: se ANOTA aparte de `escalated` para medir
		// cuántos casos entran fuera del alcance del día, y por cuál de los dos motivos (el
		// margen se puede mover con una variable; la noche no).
		if queuedNextDay {
			observability.Emit(observability.TraceSession(sess.ID), "escalacion", "encolada_proximo_dia",
				observability.EmitOpts{
					Phone: sess.PhoneNumber, Reason: preState,
					Attrs: map[string]interface{}{"motivo": queue.reason},
				})
			slog.Info("escalación encolada para el próximo día hábil",
				"session_id", sess.ID, "phone", utils.MaskPhone(sess.PhoneNumber),
				"motivo", queue.reason, "pre_state", preState)
		}

		// La MARCA del próximo día describe SIEMPRE la escalación en curso: se escribe si esta se
		// encoló y se borra si no. Pegada de una anterior le regalaría a esta las exenciones de la
		// cola (tope, cierre por silencio, no-show).
		r := sm.NewResult(sm.StateEscalated)
		// Contador del tope anti-bucle (guard (2) arriba): una escalación exitosa más en esta sesión.
		if countsTowardCap {
			r.WithContext(sm.CtxEscalationCount, bumpEscalationCount(sess))
		}
		if queuedNextDay {
			// La sesión tiene que vivir hasta que vuelva una persona: con el TTL normal vence de
			// madrugada y deja de existir para todo lo que filtra expires_at.
			r = r.WithContext(sm.CtxEscalationNextDay, "1").
				WithExpiryUntil(queuedExpiryUntil(cfg, nowFunc()))
		} else {
			r = r.WithClearCtx(sm.CtxEscalationNextDay)
		}
		// La NOTA es de ESTA escalación y ya viajó: se borra solo aquí, cuando el escalate se
		// entregó (si falla, el reintento tiene que volver a llevarla). La marca del paciente la
		// borra escalationExit en toda salida.
		return escalationExit(r).
			WithClearCtx(recovery.CtxRecoveryActive, recovery.CtxRecoveryAttempts, "escalation_note").
			WithContext("pre_escalation_state", preState).
			WithContext("escalation_team", teamID).
			WithEvent("escalated_to_agent", map[string]interface{}{
				"from_state": preState,
				"team_id":    teamID,
				"cups_code":  cupsCode,
				"patient_id": sess.GetContext("patient_id"),
			}), nil
	}
}

// ESCALATED — estado terminal especial.
// Mientras la sesión esté escalada, NO se procesan más mensajes del bot.
// El worker pool filtra mensajes de sesiones escaladas antes de llegar aquí.
func escalatedHandler() sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		// No hacer nada — el agente maneja la conversación
		return sm.NewResult(sm.StateEscalated), nil
	}
}

// resolveTeamName returns a human-readable team name from the team ID.
func resolveTeamName(teamID string, cfg *config.Config) string {
	switch teamID {
	case cfg.BirdTeamGrupoA:
		return "Grupo A (Imagenes)"
	case cfg.BirdTeamGrupoB:
		return "Grupo B (Neuro/Fisiatria)"
	default:
		return "Call Center"
	}
}

// summaryForAgent arma el resumen de contexto para el agente humano en sus dos formas a la vez:
// el texto y los grupos (que serán `facts`). El teléfono del paciente no entra en ninguno.
func summaryForAgent(sess *session.Session, cupsCode, teamName string) (string, []*summaryGroup) {
	patientName := sess.GetContext("patient_name")
	if patientName == "" {
		patientName = sess.PatientName
	}
	patientDoc := sess.GetContext("patient_doc")
	if patientDoc == "" {
		patientDoc = sess.PatientDoc
	}
	menuOption := sess.GetContext("menu_option")
	if menuOption == "" {
		menuOption = sess.MenuOption
	}
	cupsName := sess.GetContext("cups_name")
	serviceName := sess.GetContext("service_name")

	prevState := sess.GetContext("pre_escalation_state")
	if prevState == "" {
		prevState = sess.CurrentState
	}

	// Include custom escalation note if present (e.g., from identity free-text)
	note := sess.GetContext("escalation_note")

	// Un solo camino: el texto y los grupos salen de la MISMA construcción (summary_groups.go).
	// El orden de los grupos es el orden de las líneas del texto, que fijan los dorados.
	var doc summaryDoc

	doc.untitled("encabezado").raw("Transferencia de chatbot\n\n")

	patient := doc.untitled("paciente")
	patient.line(nectar.FactPatientName.Key, nectar.FactPatientName.Label, patientName)
	patient.line("documento", "Documento", patientDoc)

	if note != "" {
		doc.untitled("nota").line("nota", "Nota", note)
	}

	// Los datos del caso (entidad, orden, grupo, agenda, cita, búsqueda, intento): summaryDoc.render
	// los acomoda en el orden del resumen.
	writeCaseGroups(&doc, sess, cupsCode)

	procedure := doc.untitled("procedimiento")
	if serviceName != "" {
		procedure.line("servicio", "Servicio", serviceName)
	}
	if cupsCode != "" {
		// Línea fusionada: el texto la une, los campos van separados.
		procedure.field("cups_nombre", "Nombre", cupsName)
		procedure.field("cups_codigo", "CUPS", cupsCode)
		procedure.raw(fmt.Sprintf("Procedimiento: %s (%s)\n", cupsName, cupsCode))
	}

	state := doc.untitled("estado")
	state.line("estado_anterior", "Estado anterior", prevState)
	state.line("menu", "Menu", menuOption)
	state.tail("equipo", "Equipo", teamName)

	return doc.render()
}

// buildAgentSummary es el texto del resumen para el agente (lo que Bird recibe hoy).
func buildAgentSummary(sess *session.Session, cupsCode, teamName string) string {
	text, _ := summaryForAgent(sess, cupsCode, teamName)
	return text
}

// regFieldLabels maps registration states to human-readable field descriptions for agent commands.
var regFieldLabels = map[string]string{
	sm.StateRegDocumentType:    "tipo de documento (CC, TI, RC, CE, PA, PT, etc.)",
	sm.StateRegFirstSurname:    "primer apellido",
	sm.StateRegSecondSurname:   "segundo apellido (o NA si no tiene)",
	sm.StateRegFirstName:       "primer nombre",
	sm.StateRegSecondName:      "segundo nombre (o NA si no tiene)",
	sm.StateRegBirthDate:       "fecha de nacimiento (formato: AAAA-MM-DD)",
	sm.StateRegGender:          "genero (M o F)",
	sm.StateRegMaritalStatus:   "estado civil (SOLTERO, CASADO, UNION LIBRE, DIVORCIADO, VIUDO)",
	sm.StateRegEducationLevel:  "nivel educativo (NINGUNO, PRIMARIA, SECUNDARIA, BACHILLER, TECNICO, TECNOLOGO, UNIVERSITARIO, POSGRADO)",
	sm.StateRegOccupation:      "ocupacion u oficio (o NA)",
	sm.StateRegAddress:         "direccion completa",
	sm.StateRegPhone:           "telefono (formato colombiano, ej: 3001234567)",
	sm.StateRegPhone2:          "telefono secundario (o NA)",
	sm.StateRegEmail:           "correo electronico (o NA)",
	sm.StateRegMunicipality:    "municipio de residencia",
	sm.StateRegClientType:      "tipo de cliente (PARTICULAR, EPS, ARL, POLIZA, PREPAGADA, REGIMEN ESPECIAL)",
	sm.StateRegUserType:        "tipo de usuario (CONTRIBUTIVO, SUBSIDIADO, PARTICULAR)",
	sm.StateRegAffiliationType: "tipo de afiliacion (COTIZANTE, BENEFICIARIO, OTRO)",
	sm.StateRegEntity:          "nombre de la entidad/EPS",
	sm.StateRegZone:            "zona (U=Urbana, R=Rural)",
}

// buildAgentCommands generates contextual instructions for the agent based on escalation state.
func buildAgentCommands(sess *session.Session, cupsCode string) string {
	preState := sess.GetContext("pre_escalation_state")
	if preState == "" {
		preState = sess.CurrentState
	}

	patientName := sess.GetContext("patient_name")
	if patientName == "" {
		patientName = sess.PatientName
	}
	patientDoc := sess.GetContext("patient_doc")
	if patientDoc == "" {
		patientDoc = sess.PatientDoc
	}
	cupsName := sess.GetContext("cups_name")
	menuOption := sess.GetContext("menu_option")

	var situation, actions string

	switch preState {

	// --- Menu Principal ---
	case sm.StateMainMenu:
		situation = "El paciente no pudo seleccionar una opcion del menu principal."
		actions = "- Preguntale que necesita y selecciona por el:\n" +
			"  /bot resume MAIN_MENU agendar — Agendar cita\n" +
			"  /bot resume MAIN_MENU consultar — Consultar/cancelar citas\n" +
			"  /bot resume MAIN_MENU resultados — Ver resultados\n" +
			"  /bot resume MAIN_MENU ubicacion — Ver ubicaciones\n" +
			"  /bot resume MAIN_MENU ayuda — Ayuda\n" +
			"  /bot resume MAIN_MENU — Mostrar menu de nuevo"

	case sm.StateOutOfHoursMenu:
		situation = "El paciente intento usar el bot fuera de horario y no selecciono una opcion del menu."
		actions = "- Atiendelo o selecciona por el:\n" +
			"  /bot resume OUT_OF_HOURS_MENU ooh_resultados — Consultar resultados\n" +
			"  /bot resume OUT_OF_HOURS_MENU ooh_ubicacion — Ver ubicaciones\n" +
			"  /bot resume OUT_OF_HOURS_MENU ooh_ayuda — Ver ayuda\n" +
			"  /bot resume OUT_OF_HOURS_MENU — Mostrar menu fuera de horario de nuevo\n" +
			"  /bot cerrar"

	// --- Identificacion ---
	case sm.StateAskDocumentType:
		situation = fmt.Sprintf("El paciente no logro elegir su tipo de documento.\nMenu: %s", menuOption)
		actions = "- Preguntale el tipo de documento (CC, TI, RC, CE, PA, PT, etc.) y envialo:\n" +
			"  /bot resume ASK_DOCUMENT_TYPE CC"
	case sm.StateAskDocument:
		situation = fmt.Sprintf("El paciente no logro ingresar su numero de documento.\nMenu: %s", menuOption)
		actions = "- Preguntale su numero de documento y envialo:\n" +
			"  /bot resume ASK_DOCUMENT 1234567890\n" +
			"  (reemplaza con el documento real del paciente)"

	case sm.StateConfirmIdentity:
		situation = fmt.Sprintf("El paciente no pudo confirmar su identidad.\nPaciente: %s | Doc: %s", patientName, patientDoc)
		actions = "- Verificale los datos con el paciente y responde por el:\n" +
			"  /bot resume CONFIRM_IDENTITY identity_yes — Confirmar identidad\n" +
			"  /bot resume CONFIRM_IDENTITY identity_no — Rechazar identidad\n" +
			"  /bot resume CONFIRM_IDENTITY — Mostrar confirmacion de nuevo"

	case sm.StateConfirmContactInfo:
		phone := sess.GetContext("patient_phone")
		email := sess.GetContext("patient_email")
		situation = fmt.Sprintf("El paciente no confirmo si sus datos de contacto son correctos.\nPaciente: %s | Doc: %s\nTelefono: %s | Email: %s",
			patientName, patientDoc, phone, email)
		actions = "- Verificale los datos y responde por el:\n" +
			"  /bot resume CONFIRM_CONTACT_INFO contact_ok — Los datos son correctos\n" +
			"  /bot resume CONFIRM_CONTACT_INFO contact_update — Actualizar datos de contacto\n" +
			"  /bot resume CONFIRM_CONTACT_INFO — Mostrar datos de nuevo"

	case sm.StateAskUpdatePhone:
		situation = fmt.Sprintf("El paciente no logro actualizar su numero de telefono.\nPaciente: %s | Doc: %s", patientName, patientDoc)
		actions = "- Preguntale su telefono y envialo por el:\n" +
			"  /bot resume ASK_UPDATE_PHONE 3001234567 — Enviar telefono\n" +
			"  (reemplaza con el telefono real del paciente)"

	case sm.StateAskUpdateEmail:
		situation = fmt.Sprintf("El paciente no logro actualizar su correo electronico.\nPaciente: %s | Doc: %s", patientName, patientDoc)
		actions = "- Preguntale su email y envialo por el:\n" +
			"  /bot resume ASK_UPDATE_EMAIL correo@ejemplo.com — Enviar email\n" +
			"  /bot resume ASK_UPDATE_EMAIL NA — Si no tiene email"

	// --- Entity Management ---
	case sm.StateAskEpsRegimen:
		situation = fmt.Sprintf("El paciente no selecciono su regimen de afiliacion (contributivo o subsidiado).\nPaciente: %s | Doc: %s", patientName, patientDoc)
		actions = "- Preguntale su regimen y selecciona por el:\n" +
			"  /bot resume ASK_EPS_REGIMEN regimen_1 — Contributivo\n" +
			"  /bot resume ASK_EPS_REGIMEN regimen_2 — Subsidiado"

	case sm.StateAskClientType:
		situation = fmt.Sprintf("El paciente no pudo seleccionar su tipo de entidad (EPS, PARTICULAR, etc.).\nPaciente: %s | Doc: %s", patientName, patientDoc)
		actions = "- Preguntale su tipo de entidad y selecciona por el:\n" +
			"  /bot resume ASK_CLIENT_TYPE ct_1 — PARTICULAR\n" +
			"  /bot resume ASK_CLIENT_TYPE ct_2 — EPS\n" +
			"  /bot resume ASK_CLIENT_TYPE ct_3 — PREPAGADA\n" +
			"  /bot resume ASK_CLIENT_TYPE ct_4 — REGIMEN ESPECIAL\n" +
			"  /bot resume ASK_CLIENT_TYPE ct_5 — ARL\n" +
			"  /bot resume ASK_CLIENT_TYPE ct_6 — POLIZA\n" +
			"  /bot resume ASK_CLIENT_TYPE — Mostrar opciones al paciente de nuevo"

	case sm.StateAskEntityNumber:
		clientType := sess.GetContext("client_type")
		category := sess.GetContext("entity_category")
		situation = fmt.Sprintf("El paciente no pudo seleccionar su entidad de la lista.\nTipo: %s | Categoria: %s\nPaciente: %s | Doc: %s",
			clientType, category, patientName, patientDoc)
		actions = "- Preguntale su entidad y selecciona por el (usa el numero de la lista):\n" +
			"  /bot resume ASK_ENTITY_NUMBER 1 — Seleccionar entidad #1 de la lista\n" +
			"  /bot resume ASK_ENTITY_NUMBER 5 — Seleccionar entidad #5 de la lista\n" +
			"  /bot resume ASK_ENTITY_NUMBER — Mostrar lista de nuevo\n" +
			"  /bot resume ASK_CLIENT_TYPE — Cambiar tipo de entidad"

	case sm.StateConfirmEntity:
		patientEntity := sess.GetContext("patient_entity")
		situation = fmt.Sprintf("El paciente no pudo confirmar su entidad.\nEntidad actual: %s\nPaciente: %s | Doc: %s",
			patientEntity, patientName, patientDoc)
		actions = "- Verificale la entidad con el paciente y responde por el:\n" +
			"  /bot resume CONFIRM_ENTITY entity_ok — Confirmar entidad\n" +
			"  /bot resume CONFIRM_ENTITY entity_change — Cambiar entidad\n" +
			"  /bot resume CONFIRM_ENTITY — Mostrar confirmacion de nuevo"

	case sm.StateChangeEntity:
		patientEntity := sess.GetContext("patient_entity")
		situation = fmt.Sprintf("El paciente no encontro su entidad al buscarla.\nEntidad actual: %s\nPaciente: %s | Doc: %s",
			patientEntity, patientName, patientDoc)
		actions = "- Preguntale el nombre de su entidad y busca por el:\n" +
			"  /bot resume CHANGE_ENTITY nombre de la entidad — Buscar entidad\n" +
			"  Ej: /bot resume CHANGE_ENTITY SANITAS\n" +
			"  /bot resume ASK_CLIENT_TYPE — Cambiar tipo de entidad"

	case sm.StateShowEntityList:
		clientType := sess.GetContext("client_type")
		category := sess.GetContext("entity_category")
		situation = fmt.Sprintf("No se encontraron entidades/EPS para la categoria seleccionada.\nTipo: %s | Categoria: %s\nPaciente: %s | Doc: %s",
			clientType, category, patientName, patientDoc)
		actions = "- Verifica la entidad del paciente y luego:\n" +
			"  /bot resume CHECK_ENTITY — Reintentar validacion de entidad\n" +
			"  /bot cerrar — Cerrar conversacion"

	// --- Registro de paciente ---
	case sm.StateRegistrationStart:
		situation = fmt.Sprintf("El paciente no decidio si registrarse como nuevo.\nDoc: %s", patientDoc)
		actions = "- Preguntale si quiere registrarse y responde por el:\n" +
			"  /bot resume REGISTRATION_START register_yes — Iniciar registro\n" +
			"  /bot resume REGISTRATION_START register_no — No registrarse\n" +
			"  /bot resume REGISTRATION_START — Preguntar de nuevo\n" +
			"  /bot cerrar"

	case sm.StateConfirmRegistration:
		situation = fmt.Sprintf("El paciente no confirmo sus datos de registro.\nPaciente: %s | Doc: %s",
			sess.GetContext("reg_first_name")+" "+sess.GetContext("reg_first_surname"), patientDoc)
		actions = "- Verificale los datos con el paciente y responde por el:\n" +
			"  /bot resume CONFIRM_REGISTRATION reg_confirm — Confirmar y crear paciente\n" +
			"  /bot resume CONFIRM_REGISTRATION reg_correct — Corregir algun dato\n" +
			"  /bot resume CONFIRM_REGISTRATION — Mostrar resumen de nuevo"

	case sm.StateRegSelectCorrection:
		situation = fmt.Sprintf("El paciente no selecciono que dato desea corregir.\nPaciente: %s | Doc: %s",
			sess.GetContext("reg_first_name")+" "+sess.GetContext("reg_first_surname"), patientDoc)
		actions = "- Preguntale que dato quiere corregir y selecciona por el:\n" +
			"  /bot resume REG_SELECT_CORRECTION corr_first_name — Primer nombre\n" +
			"  /bot resume REG_SELECT_CORRECTION corr_first_surname — Primer apellido\n" +
			"  /bot resume REG_SELECT_CORRECTION corr_birth_date — Fecha de nacimiento\n" +
			"  /bot resume REG_SELECT_CORRECTION corr_address — Direccion\n" +
			"  /bot resume REG_SELECT_CORRECTION corr_phone — Telefono\n" +
			"  /bot resume REG_SELECT_CORRECTION corr_email — Email\n" +
			"  /bot resume REG_SELECT_CORRECTION corr_document_type — Tipo de documento\n" +
			"  /bot resume REG_SELECT_CORRECTION corr_marital_status — Estado civil\n" +
			"  /bot resume REG_SELECT_CORRECTION corr_user_type — Tipo de usuario\n" +
			"  /bot resume REG_SELECT_CORRECTION corr_restart — Empezar registro de nuevo\n" +
			"  /bot resume REG_SELECT_CORRECTION — Mostrar lista de campos de nuevo"

	// --- Orden Medica ---
	case sm.StateAskMedicalOrder:
		situation = fmt.Sprintf("El paciente necesita enviar su orden medica.\nPaciente: %s | Doc: %s", patientName, patientDoc)
		actions = "- Pidele que envie la foto de la orden:\n" +
			"  /bot resume UPLOAD_MEDICAL_ORDER — Pedir foto de orden\n" +
			"- Si conoces el codigo CUPS directamente:\n" +
			"  /bot cups <codigo>[:cantidad] ...\n" +
			"  Ej: /bot cups 883141\n" +
			"  Ej: /bot cups 883141:1 930810:2\n" +
			"- Si el paciente te describe la orden (sin codigo):\n" +
			"  /bot orden <descripcion de procedimientos>\n" +
			"  Ej: /bot orden Resonancia cerebral simple codigo 883141, cantidad 1"

	case sm.StateUploadMedicalOrder:
		situation = fmt.Sprintf("El paciente no logro enviar la foto de su orden medica o el bot no la reconocio.\nPaciente: %s | Doc: %s", patientName, patientDoc)
		actions = "- Pidele que envie la foto de la orden:\n" +
			"  /bot resume UPLOAD_MEDICAL_ORDER — Reintentar subida de orden\n" +
			"- Si conoces el codigo CUPS directamente:\n" +
			"  /bot cups <codigo>[:cantidad] ...\n" +
			"  Ej: /bot cups 883141\n" +
			"  Ej: /bot cups 883141:1 930810:2\n" +
			"- Si el paciente te describe la orden (sin codigo):\n" +
			"  /bot orden Resonancia cerebral simple codigo 883141, cantidad 1"

	case sm.StateConfirmOCRResult:
		ocrCups := sess.GetContext("ocr_cups_json")
		situation = fmt.Sprintf("El paciente no confirmo el resultado del reconocimiento de la orden.\nCUPS detectados: %s\nPaciente: %s | Doc: %s",
			ocrCups, patientName, patientDoc)
		actions = "- Verificale los procedimientos detectados con el paciente y responde por el:\n" +
			"  /bot resume CONFIRM_OCR_RESULT ocr_correct — Los procedimientos son correctos\n" +
			"  /bot resume CONFIRM_OCR_RESULT ocr_incorrect — Los procedimientos son incorrectos\n" +
			"  /bot resume CONFIRM_OCR_RESULT — Mostrar resultado de nuevo\n" +
			"- Si quieres corregir con el codigo CUPS exacto:\n" +
			"  /bot cups <codigo>[:cantidad] ...\n" +
			"  Ej: /bot cups 883141\n" +
			"- Si el paciente te describe la orden (sin codigo):\n" +
			"  /bot orden <descripcion de procedimientos con codigos y cantidades>"

	case sm.StateAskManualCups:
		situation = fmt.Sprintf("El paciente no pudo encontrar su procedimiento al buscarlo.\nPaciente: %s | Doc: %s", patientName, patientDoc)
		actions = "- Si conoces el codigo CUPS:\n" +
			"  /bot cups <codigo>[:cantidad] ...\n" +
			"  Ej: /bot cups 883141\n" +
			"- Si el paciente te describe la orden:\n" +
			"  /bot orden <descripcion con codigos y cantidades>\n" +
			"- O busca por nombre:\n" +
			"  /bot resume ASK_MANUAL_CUPS nombre del procedimiento\n" +
			"  Ej: /bot resume ASK_MANUAL_CUPS resonancia cerebral simple"

	case sm.StateSelectProcedure:
		situation = fmt.Sprintf("El paciente no logro seleccionar un procedimiento de la lista.\nPaciente: %s | Doc: %s", patientName, patientDoc)
		actions = "- Preguntale que procedimiento necesita:\n" +
			"  /bot resume ASK_MANUAL_CUPS nombre del procedimiento\n" +
			"  Ej: /bot resume ASK_MANUAL_CUPS resonancia cerebral\n" +
			"  /bot resume SELECT_PROCEDURE — Mostrar lista de nuevo"

	// --- Validaciones Medicas ---
	case sm.StateAskGestationalWeeks:
		gestRange := ""
		if gr, ok := pregnancyUltrasoundCups[cupsCode]; ok {
			gestRange = gr.label
		}
		situation = fmt.Sprintf("Ecografia obstetrica — el bot espera que la paciente indique su semana de gestacion exacta.\nProcedimiento: %s (%s)\nRango requerido: %s\nPaciente: %s",
			cupsName, cupsCode, gestRange, patientName)
		actions = fmt.Sprintf("- Preguntale a la paciente cuantas semanas de gestacion tiene. El rango requerido es: %s\n", gestRange) +
			"  /bot resume ASK_GESTATIONAL_WEEKS 13 — Enviar semana exacta (ej: 13, 11.5, 12)\n" +
			"  /bot resume ASK_GESTATIONAL_WEEKS weeks_yes — Paciente SI esta en el rango (sin filtrar por fecha)\n" +
			"  /bot resume ASK_GESTATIONAL_WEEKS weeks_no — Paciente NO esta en el rango\n" +
			"  /bot resume ASK_GESTATIONAL_WEEKS — Preguntar de nuevo"

	case sm.StateAskContrasted:
		situation = fmt.Sprintf("El paciente no respondio si el examen es con o sin contraste.\nProcedimiento: %s (%s)\nPaciente: %s",
			cupsName, cupsCode, patientName)
		actions = "- Preguntale si es con o sin contraste:\n" +
			"  /bot resume ASK_CONTRASTED contrast_yes — Con contraste\n" +
			"  /bot resume ASK_CONTRASTED contrast_no — Sin contraste"

	case sm.StateAskPregnancy:
		situation = fmt.Sprintf("Paciente femenina con contraste — no respondio si esta embarazada.\nProcedimiento: %s\nPaciente: %s | Edad: %s",
			cupsName, patientName, sess.GetContext("patient_age"))
		actions = "- Preguntale si esta embarazada:\n" +
			"  /bot resume ASK_PREGNANCY pregnant_no — No embarazada\n" +
			"  /bot resume ASK_PREGNANCY pregnant_yes — Embarazada (bloquea cita)"

	case sm.StateAskBabyWeight:
		situation = fmt.Sprintf("Bebe <1 ano con contraste — no indico peso del bebe.\nPaciente: %s | Edad: %s",
			patientName, sess.GetContext("patient_age"))
		actions = "- Preguntale el peso del bebe:\n" +
			"  /bot resume ASK_BABY_WEIGHT baby_normal — Peso normal\n" +
			"  /bot resume ASK_BABY_WEIGHT baby_low — Bajo peso"

	case sm.StateGfrCreatinine:
		situation = fmt.Sprintf("El paciente no ingreso su valor de creatinina (examen con contraste).\nProcedimiento: %s\nPaciente: %s",
			cupsName, patientName)
		actions = "- Preguntale su creatinina (mg/dL):\n" +
			"  /bot resume GFR_CREATININE 0.96 — Enviar valor\n" +
			"  /bot resume GFR_CREATININE — Preguntar de nuevo"

	case sm.StateGfrHeight:
		situation = fmt.Sprintf("Paciente pediatrico — no ingreso estatura (30-250 cm).\nPaciente: %s | Edad: %s | Creatinina: %s",
			patientName, sess.GetContext("patient_age"), sess.GetContext("gfr_creatinine"))
		actions = "- Preguntale la estatura en cm:\n" +
			"  /bot resume GFR_HEIGHT 120 — Enviar estatura\n" +
			"  /bot resume GFR_HEIGHT — Preguntar de nuevo"

	case sm.StateGfrWeight:
		situation = fmt.Sprintf("El paciente no ingreso su peso (10-300 kg).\nPaciente: %s | Creatinina: %s",
			patientName, sess.GetContext("gfr_creatinine"))
		actions = "- Preguntale el peso en kg:\n" +
			"  /bot resume GFR_WEIGHT 70 — Enviar peso\n" +
			"  /bot resume GFR_WEIGHT — Preguntar de nuevo"

	case sm.StateGfrDisease:
		situation = fmt.Sprintf("El paciente (15-39 anos) no selecciono tipo de enfermedad para calculo GFR.\nPaciente: %s | Edad: %s | Creatinina: %s",
			patientName, sess.GetContext("patient_age"), sess.GetContext("gfr_creatinine"))
		actions = "- Preguntale si tiene alguna enfermedad:\n" +
			"  /bot resume GFR_DISEASE disease_none — Sin enfermedad\n" +
			"  /bot resume GFR_DISEASE disease_renal — Enfermedad renal\n" +
			"  /bot resume GFR_DISEASE disease_diabetica — Diabetes"

	case sm.StateAskSedation:
		situation = fmt.Sprintf("El paciente no respondio si requiere sedacion para resonancia.\nProcedimiento: %s (%s)\nPaciente: %s",
			cupsName, cupsCode, patientName)
		actions = "- Preguntale si necesita sedacion:\n" +
			"  /bot resume ASK_SEDATION sedated_yes — Con sedacion\n" +
			"  /bot resume ASK_SEDATION sedated_no — Sin sedacion"

	case sm.StateCheckSpecialCups:
		if isSleepStudy(cupsCode) {
			situation = fmt.Sprintf("Estudio del sueno — requiere coordinacion especial.\nProcedimiento: %s (%s)\nPaciente: %s | Doc: %s | Edad: %s",
				cupsName, cupsCode, patientName, patientDoc, sess.GetContext("patient_age"))
			actions = "- Coordina la cita manualmente y luego:\n" +
				"  /bot cerrar — Cerrar cuando termines"
		} else {
			situation = fmt.Sprintf("Procedimiento especial requiere atencion manual.\nProcedimiento: %s (%s)\nPaciente: %s | Doc: %s",
				cupsName, cupsCode, patientName, patientDoc)
			actions = "- Gestiona el procedimiento manualmente:\n" +
				"  /bot cerrar — Cerrar cuando termines"
		}

	// --- Slots y Reserva ---
	case sm.StateShowSlots:
		situation = fmt.Sprintf("El paciente no logro seleccionar un horario para su cita.\nProcedimiento: %s (%s)", cupsName, cupsCode)
		actions = "- Preguntale que horario prefiere y selecciona por el (usa el numero del horario):\n" +
			"  /bot resume SHOW_SLOTS 1 — Seleccionar horario #1 de la lista\n" +
			"  /bot resume SHOW_SLOTS 3 — Seleccionar horario #3 de la lista\n" +
			"  /bot resume SHOW_SLOTS — Mostrar horarios de nuevo\n" +
			"  /bot resume SEARCH_SLOTS — Buscar nuevos horarios"

	case sm.StateOfferWaitingList:
		situation = fmt.Sprintf("No hay horarios disponibles — no respondio si quiere lista de espera.\nProcedimiento: %s\nPaciente: %s",
			cupsName, patientName)
		actions = "- Preguntale si quiere lista de espera:\n" +
			"  /bot resume OFFER_WAITING_LIST wl_yes — Agregar a lista\n" +
			"  /bot resume OFFER_WAITING_LIST wl_no — No agregar\n" +
			"  /bot resume SEARCH_SLOTS — Buscar horarios de nuevo"

	case sm.StateConfirmBooking:
		situation = fmt.Sprintf("El paciente no confirmo la reserva del horario seleccionado.\nProcedimiento: %s\nPaciente: %s",
			cupsName, patientName)
		actions = "- Preguntale si confirma el horario y responde por el:\n" +
			"  /bot resume CONFIRM_BOOKING booking_confirm — Confirmar reserva\n" +
			"  /bot resume CONFIRM_BOOKING booking_change — Cambiar horario\n" +
			"  /bot resume CONFIRM_BOOKING — Mostrar confirmacion de nuevo\n" +
			"  /bot resume SEARCH_SLOTS — Buscar otros horarios"

	case sm.StateReconfirmBooking:
		situation = fmt.Sprintf("El paciente no reconfirmo la reserva (segunda confirmacion).\nProcedimiento: %s\nPaciente: %s",
			cupsName, patientName)
		actions = "- Preguntale si confirma definitivamente y responde por el:\n" +
			"  /bot resume RECONFIRM_BOOKING reconfirm_yes — Si, confirmar definitivamente\n" +
			"  /bot resume RECONFIRM_BOOKING reconfirm_no — No, volver a horarios\n" +
			"  /bot resume RECONFIRM_BOOKING — Mostrar confirmacion de nuevo"

	// --- Citas Existentes ---
	case sm.StateNoAppointments:
		situation = fmt.Sprintf("El paciente no tiene citas pendientes o confirmadas.\nPaciente: %s | Doc: %s", patientName, patientDoc)
		actions = "- Atiende al paciente y responde por el:\n" +
			"  /bot resume NO_APPOINTMENTS no_appt_menu — Volver al menu principal\n" +
			"  /bot resume NO_APPOINTMENTS no_appt_end — Terminar chat\n" +
			"  /bot resume NO_APPOINTMENTS — Mostrar opciones de nuevo"

	case sm.StateListAppointments:
		situation = fmt.Sprintf("El paciente no logro seleccionar una cita de la lista.\nPaciente: %s | Doc: %s", patientName, patientDoc)
		actions = "- Muestra la lista de nuevo para que el paciente seleccione:\n" +
			"  /bot resume LIST_APPOINTMENTS — Mostrar lista de citas de nuevo\n" +
			"  /bot resume FETCH_APPOINTMENTS — Recargar citas desde el sistema\n" +
			"- Si el paciente indica cual cita quiere (por numero en la lista):\n" +
			"  /bot resume LIST_APPOINTMENTS 1 — Ver detalle de la cita #1\n" +
			"  /bot resume LIST_APPOINTMENTS 2 — Ver detalle de la cita #2\n" +
			"- Si el paciente dice que la fecha/hora esta mal o tiene mas citas:\n" +
			"  /bot resume FETCH_APPOINTMENTS — Recargar y mostrar lista actualizada"

	case sm.StateAppointmentAction:
		situation = fmt.Sprintf("El paciente no selecciono accion sobre su cita.\nPaciente: %s | Cita: %s",
			patientName, sess.GetContext("selected_appointment_id"))
		actions = "- Preguntale que quiere hacer con la cita y selecciona por el:\n" +
			"  /bot resume APPOINTMENT_ACTION appt_confirm — Confirmar cita\n" +
			"  /bot resume APPOINTMENT_ACTION appt_cancel — Cancelar cita\n" +
			"  /bot resume APPOINTMENT_ACTION appt_reschedule — Reagendar cita\n" +
			"  /bot resume APPOINTMENT_ACTION appt_preparation — Ver preparacion\n" +
			"  /bot resume APPOINTMENT_ACTION appt_back — Volver a lista de citas\n" +
			"  /bot resume APPOINTMENT_ACTION appt_menu — Ir al menu principal\n" +
			"  /bot resume LIST_APPOINTMENTS — Mostrar lista de citas de nuevo"

	case sm.StateConfirmAppointment:
		situation = fmt.Sprintf("El paciente no confirmo la accion de confirmar cita.\nPaciente: %s", patientName)
		actions = "- Preguntale si confirma la cita y responde por el:\n" +
			"  /bot resume CONFIRM_APPOINTMENT confirm_yes — Si, confirmar cita\n" +
			"  /bot resume CONFIRM_APPOINTMENT confirm_no — No, cancelar accion\n" +
			"  /bot resume CONFIRM_APPOINTMENT — Mostrar confirmacion de nuevo\n" +
			"  /bot resume LIST_APPOINTMENTS — Volver a lista"

	case sm.StateCancelAppointment:
		situation = fmt.Sprintf("El paciente no confirmo la cancelacion de su cita.\nPaciente: %s", patientName)
		actions = "- Preguntale si desea cancelar y responde por el:\n" +
			"  /bot resume CANCEL_APPOINTMENT cancel_yes — Si, cancelar cita\n" +
			"  /bot resume CANCEL_APPOINTMENT cancel_no — No, mantener cita\n" +
			"  /bot resume CANCEL_APPOINTMENT — Mostrar confirmacion de nuevo\n" +
			"  /bot resume LIST_APPOINTMENTS — Volver a lista"

	case sm.StateCancelReason:
		situation = fmt.Sprintf("El paciente no selecciono motivo de cancelacion.\nPaciente: %s", patientName)
		actions = "- Preguntale el motivo:\n" +
			"  /bot resume CANCEL_REASON — Mostrar motivos de nuevo\n" +
			"  /bot resume LIST_APPOINTMENTS — Volver a lista"

	// --- Notificación proactiva — texto libre repetido (escalado por HandleInvalidInput) ---
	case sm.StateNotifPending:
		apptDate := sess.GetContext("notif_appt_date")
		apptTime := sess.GetContext("notif_appt_time")
		cupsNotif := sess.GetContext("notif_cups_name")
		situation = fmt.Sprintf("Paciente envio texto libre reiteradamente al recibir template de confirmacion.\n"+
			"No presiono ningun boton. Cita: %s %s — %s\nPaciente: %s",
			apptDate, apptTime, cupsNotif, patientName)
		actions = "- Preguntale al paciente que desea hacer y responde por el:\n" +
			"  /bot resume NOTIF_PENDING confirm — Confirmar la cita\n" +
			"  /bot resume NOTIF_PENDING reschedule — Reprogramar la cita\n" +
			"  /bot resume NOTIF_PENDING cancel — Cancelar la cita"

	// --- Notificaciones proactivas (reprogramar / cancelar desde template) ---
	case sm.StateConfirmRescheduleNotif:
		apptDate := sess.GetContext("notif_appt_date")
		apptTime := sess.GetContext("notif_appt_time")
		cupsNotif := sess.GetContext("notif_cups_name")
		situation = fmt.Sprintf("El paciente recibio notificacion de reprogramacion y no respondio.\nCita: %s %s — %s\nPaciente: %s",
			apptDate, apptTime, cupsNotif, patientName)
		actions = "- Preguntale si desea reprogramar y responde por el:\n" +
			"  /bot resume CONFIRM_RESCHEDULE_NOTIF reschedule_yes — Si, buscar nuevos horarios\n" +
			"  /bot resume CONFIRM_RESCHEDULE_NOTIF reschedule_no — No, mantener la cita\n" +
			"  /bot resume CONFIRM_RESCHEDULE_NOTIF — Mostrar confirmacion de nuevo"

	case sm.StateConfirmCancelNotif:
		apptDate := sess.GetContext("notif_appt_date")
		apptTime := sess.GetContext("notif_appt_time")
		cupsNotif := sess.GetContext("notif_cups_name")
		situation = fmt.Sprintf("El paciente recibio notificacion de cancelacion y no respondio.\nCita: %s %s — %s\nPaciente: %s",
			apptDate, apptTime, cupsNotif, patientName)
		actions = "- Preguntale si desea cancelar y responde por el:\n" +
			"  /bot resume CONFIRM_CANCEL_NOTIF cancel_yes — Si, cancelar la cita\n" +
			"  /bot resume CONFIRM_CANCEL_NOTIF cancel_no — No, mantener la cita\n" +
			"  /bot resume CONFIRM_CANCEL_NOTIF — Mostrar confirmacion de nuevo"

	case sm.StateNotifRescheduleFallback:
		apptDate := sess.GetContext("notif_appt_date")
		apptTime := sess.GetContext("notif_appt_time")
		cupsNotif := sess.GetContext("notif_cups_name")
		situation = fmt.Sprintf("Paciente intento reprogramar pero no hay slots disponibles.\nCita: %s %s — %s\nPaciente: %s",
			apptDate, apptTime, cupsNotif, patientName)
		actions = "- Preguntale si desea confirmar o cancelar la cita:\n" +
			"  /bot resume NOTIF_RESCHEDULE_FALLBACK confirm — Confirmar la cita\n" +
			"  /bot resume NOTIF_RESCHEDULE_FALLBACK cancel — Cancelar la cita"

	// --- Bot desactivado (BOT_ENABLED=false) ---
	case sm.BotDisabledStep:
		return "Situacion: Bot desactivado. El paciente aún no fue atendido.\n\n" +
			"Acciones sugeridas:\n" +
			"- Atiende al paciente directamente desde el inicio.\n\n" +
			"/bot cerrar — Cerrar conversacion\n" +
			"/bot info — Ver contexto completo"

	// --- Post-Accion y Cierre ---
	case sm.StatePostActionMenu:
		situation = "El paciente solicito hablar con un agente directamente."
		if menuOption != "" || cupsName != "" {
			details := []string{}
			if menuOption != "" {
				details = append(details, "Menu: "+menuOption)
			}
			if cupsName != "" {
				details = append(details, fmt.Sprintf("Procedimiento: %s (%s)", cupsName, cupsCode))
			}
			situation += "\n" + strings.Join(details, " | ")
		}
		actions = "- Atiende al paciente. Cuando termines puedes:\n" +
			"  /bot resume POST_ACTION_MENU ver_citas — Devolver a consulta de citas\n" +
			"  /bot resume POST_ACTION_MENU menu_principal — Devolver al menu principal\n" +
			"  /bot resume POST_ACTION_MENU terminar_chat — Finalizar conversacion\n" +
			"  /bot resume POST_ACTION_MENU — Mostrar menu de acciones de nuevo\n" +
			"  /bot cerrar — Cerrar conversacion"

	case sm.StateFallbackMenu:
		situation = "El paciente llego al menu de recuperacion (reintentos agotados o error)."
		if patientName != "" {
			situation += fmt.Sprintf("\nPaciente: %s | Doc: %s", patientName, patientDoc)
		}
		actions = "- Atiende al paciente y luego:\n" +
			"  /bot — Reiniciar desde cero\n" +
			"  /bot cerrar"

	default:
		// Check if it's a registration field state (REG_*)
		if label, ok := regFieldLabels[preState]; ok {
			situation = fmt.Sprintf("El paciente no pudo ingresar: %s\nEstado: %s\nPaciente: %s | Doc: %s",
				label, preState, patientName, patientDoc)
			actions = fmt.Sprintf("- Preguntale el dato y envialo:\n"+
				"  /bot resume %s dato — Enviar dato correcto\n"+
				"  /bot resume %s — Pedir dato de nuevo\n"+
				"  Ej: /bot resume %s valor_del_dato", preState, preState, preState)
		} else {
			situation = fmt.Sprintf("El paciente tuvo dificultades en el paso: %s", preState)
			if patientName != "" {
				situation += fmt.Sprintf("\nPaciente: %s | Doc: %s", patientName, patientDoc)
			}
			actions = fmt.Sprintf("- Preguntale que necesita y usa:\n"+
				"  /bot resume %s — Reintentar este paso\n"+
				"  /bot resume %s dato — Enviar dato corregido", preState, preState)
		}
	}

	return fmt.Sprintf("Situacion: %s\n\nAcciones sugeridas:\n%s\n\n"+
		"/bot — Reiniciar desde menu\n"+
		"/bot cerrar — Cerrar conversacion\n"+
		"/bot info — Ver contexto completo", situation, actions)
}
