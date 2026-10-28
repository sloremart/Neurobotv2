package worker

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/observability"
	"github.com/neuro-bot/neuro-bot/internal/session"
	"github.com/neuro-bot/neuro-bot/internal/statemachine"
	"github.com/neuro-bot/neuro-bot/internal/utils"
)

// agentCommandResumeProblem es el motivo, para el asesor, de un comando /bot cups u orden que no
// pudo retomar la sesión.
const agentCommandResumeProblem = "El bot no pudo retomar la sesión; vuelva a devolver la conversación."

// agentCloseFarewell es la despedida al paciente cuando el agente da el caso por resuelto.
const agentCloseFarewell = "Tu consulta ha sido resuelta. Gracias por comunicarte con nosotros!"

// closeNectarConversation es la mitad de handleAgentClose propia de Néctar (la sesión ya está
// completada cuando se llama).
//
// Con motivo (cmd.Reason, de conversacion_resuelta) la resolvió el agente en Néctar y ya cerró
// con el paciente: el bot no se despide otra vez —además, la conversación ya no es suya y el
// envío no saldría—. Sin motivo el cierre lo decidió el agente desde el menú de retornos, la
// conversación volvió al bot, y la despedida sí le toca a él.
//
// El cierre es SÍNCRONO, a diferencia del de Bird (que lo aparca 3 s para que Bird alcance a
// entregar la despedida): con Néctar el envío es un POST que ya volvió con el id del mensaje, así
// que no queda nada en vuelo que el cierre pueda cortar.
func (p *MessageWorkerPool) closeNectarConversation(sess *session.Session, cmd AgentCommand) {
	if cmd.Reason == "" {
		if _, err := p.birdClient.SendText(sess.PhoneNumber, sess.ConversationID, agentCloseFarewell); err != nil {
			slog.Warn("farewell on agent close failed (nectar)",
				"conversation_id", sess.ConversationID, "session_id", sess.ID, "error", err)
		}
	}
	if err := p.birdClient.CloseFeedItems(sess.PhoneNumber, sess.ConversationID); err != nil {
		slog.Warn("close conversation on agent close failed (nectar)",
			"conversation_id", sess.ConversationID, "session_id", sess.ID, "reason", cmd.Reason, "error", err)
	}
}

// clearNextDayQueueMark borra la marca de «escalación encolada para el próximo día hábil»
// (statemachine.CtxEscalationNextDay) cuando esa espera TERMINA: una persona tomó el caso —lo
// devolvió o lo resolvió— o el bot lo retomó.
//
// La marca describe SIEMPRE la escalación en curso; dejarla pegada después le atribuiría a lo
// que le pase luego a la misma sesión una espera que ya terminó.
//
// Solo escribe si la marca está: no se paga un UPDATE por cada retorno de agente.
func (p *MessageWorkerPool) clearNextDayQueueMark(ctx context.Context, sess *session.Session) {
	if sess.GetContext(statemachine.CtxEscalationNextDay) == "" {
		return
	}
	if err := p.sessionManager.SaveState(ctx, sess, sess.CurrentState, nil,
		[]string{statemachine.CtxEscalationNextDay}); err != nil {
		slog.Warn("no se pudo borrar la marca de escalación encolada",
			"session_id", sess.ID, "phone", utils.MaskPhone(sess.PhoneNumber), "error", err)
	}
}

// resolveAgentReturn traduce lo que el agente devolvió, en el dialecto o en el protocolo v1, y
// devuelve las dos mitades que handleAgentResume necesita: el veredicto sobre lo ESCRITO y el
// TOKEN con el que se consulta el menú del paso.
//
// La precedencia es la del contrato: si el aviso trajo el retorno v1, manda ése —el dialecto
// puede venir a su lado, y entonces es la copia vieja del mismo hecho—.
func (p *MessageWorkerPool) resolveAgentReturn(sess *session.Session, step string,
	cmd AgentCommand,
) (statemachine.ReturnInputResult, string) {
	if cmd.ReturnV1 != nil {
		// `valores` NO se traza: puede llevar el documento del paciente. `opcion` va por
		// ReturnValueForLog, por la misma regla que el token del dialecto: si trae un dato
		// escrito, solo sale su prefijo.
		slog.Info("agent return: retorno v1", "phone", utils.MaskPhone(cmd.Phone),
			"session_id", sess.ID, "step", step, "opcion", statemachine.ReturnValueForLog(cmd.ReturnV1.Option),
			"con_valores", len(cmd.ReturnV1.Values) > 0)
		return p.machine.ResolveReturnV1(sess, step, cmd.ReturnV1.Values), cmd.ReturnV1.Option
	}
	return p.machine.ResolveReturnInput(sess, step, cmd.ReturnValue), cmd.ReturnValue
}

// applyAgentReturn aplica la decisión que el agente tomó en Néctar al devolver la conversación.
// El token se traduce contra el MISMO paso desde el que se ofreció el menú; se resuelve aquí, y
// no en el manejador del aviso, porque la traducción necesita la sesión.
//
// Devuelve el comando ajustado (estado y dato con los que reanudar) y done=true cuando el retorno
// ya se atendió entero —comando del asesor, re-escalada o cierre— y no queda nada por reanudar.
func (p *MessageWorkerPool) applyAgentReturn(ctx context.Context, sess *session.Session,
	cmd AgentCommand,
) (AgentCommand, bool) {
	step := sess.GetContext("pre_escalation_state")
	if step == "" {
		step = sess.CurrentState
	}
	// Primero: ¿el agente ESCRIBIÓ la respuesta? Se mira antes que el menú porque un dato escrito
	// no es un token y no se debe buscar entre ellos. En v1 el dato viaja en `valores`, aparte del
	// token; los tres desenlaces son los mismos, por eso las dos formas desembocan en este switch.
	written, token := p.resolveAgentReturn(sess, step, cmd)
	opt, ok := statemachine.ResolveReturnValue(sess, step, token)
	switch {
	case written.OK():
		cmd.Data = written.Value
		if written.Command != "" {
			// El paso entrega el dato al comando del asesor (lista de CUPS u orden en texto)
			// en vez de inyectarlo: ese comando es quien sabe consumirlo.
			slog.Info("agent return: el dato escrito va al comando del asesor",
				"phone", utils.MaskPhone(cmd.Phone), "session_id", sess.ID, "step", step,
				"command", written.Command)
			var problem string
			switch written.Command {
			case statemachine.ReturnCommandCups:
				problem = p.handleAgentCups(ctx, sess, cmd)
			case statemachine.ReturnCommandOrder:
				problem = p.handleAgentOrder(ctx, sess, cmd)
			}
			// Si el comando no pudo con el dato, su aviso al asesor es una nota interna que en
			// Néctar no tiene dónde aterrizar: la sesión quedaría escalada sin dueño. El fallo
			// vuelve entonces a una persona como cualquier dato rechazado.
			if problem != "" && session.IsNectarSession(sess.PhoneNumber) {
				written.Problem = problem
				p.reescalateRejectedAgentInput(ctx, sess, cmd, step, written)
			}
			return cmd, true
		}
		// Se inyecta como mensaje virtual: el paso lo valida como si lo hubiera escrito el
		// paciente. Hay pasos que no saben recibir lo que trae el agente, y entonces el dato se
		// inyecta donde sí se sabe consumir (Target).
		if written.Target != "" {
			cmd.State = written.Target
		}
		slog.Info("agent return: el agente escribió la respuesta",
			"phone", utils.MaskPhone(cmd.Phone), "session_id", sess.ID, "step", step,
			"target_state", cmd.State)
	case written.Rejected():
		// El dato del agente no sirve. NO se le pasa el problema al paciente: vuelve a una
		// persona con el motivo escrito.
		p.reescalateRejectedAgentInput(ctx, sess, cmd, step, written)
		return cmd, true
	case !ok:
		// Token que no pertenece a este paso (menú viejo, o el paso cambió entre el escalate y la
		// vuelta). NO se adivina una rama: se reanuda sin decisión, que es lo mismo que pasa
		// cuando el agente devuelve sin elegir.
		// ⚠️ EL TOKEN VA POR SU PREFIJO Y NUNCA CON EL VALOR: en el dialecto un dato escrito es
		// `bot_dato:<lo que tecleó el agente>` —en ASK_DOCUMENT, el documento del paciente— y no
		// entra en ningún registro.
		slog.Warn("agent return: token desconocido para el paso, se reanuda sin decisión",
			"phone", utils.MaskPhone(cmd.Phone), "session_id", sess.ID,
			"step", step, "return_value", statemachine.ReturnValueForLog(token))
	case opt.Close:
		slog.Info("agent return: el agente cerró el caso", "phone", utils.MaskPhone(cmd.Phone),
			"session_id", sess.ID, "step", step, "return_value", token)
		p.handleAgentClose(ctx, sess, cmd)
		return cmd, true
	default:
		if opt.Target != "" {
			cmd.State = opt.Target
		}
		if opt.Data != "" {
			cmd.Data = opt.Data
		}
		slog.Info("agent return: retomando por la salida elegida", "phone", utils.MaskPhone(cmd.Phone),
			"session_id", sess.ID, "step", step, "return_value", token,
			"target_state", cmd.State, "data", cmd.Data != "")
	}
	return cmd, false
}

// rejectedInputEchoMaxLen acota el eco del valor rechazado en la nota que lee el agente: con
// ~120 caracteres le alcanza para reconocer su propio error, y un texto largo devuelto entero
// solo inflaría el resumen.
const rejectedInputEchoMaxLen = 120

// truncateRejectedEcho recorta (rune-safe) el ECO del valor en la nota de un dato rechazado. Solo
// afecta lo que el agente LEE; res.Value sigue completo para cualquier otro uso.
func truncateRejectedEcho(s string) string {
	r := []rune(s)
	if len(r) <= rejectedInputEchoMaxLen {
		return s
	}
	return string(r[:rejectedInputEchoMaxLen]) + "…"
}

// reescalateRejectedAgentInput devuelve a una PERSONA el caso cuyo dato escrito no sirvió.
//
// La alternativa era reanudar el paso y volver a preguntarle al paciente. Se descartó: el
// paciente ya lleva esperando desde la primera escalación, no cometió ningún error, y devolverle
// la pregunta convierte el descuido del agente en trabajo suyo.
//
// NO tiene que ser el mismo agente: el motivo viaja en el resumen, así que el caso llega completo
// a quien lo tome. Ningún tope del bot acota cuántas vueltas puede dar un agente que insista con
// datos malos: lo que las acota es que cada una exige que una PERSONA devuelva la conversación a
// mano, no un lazo del bot.
//
// El contexto de la re-escalada se PERSISTE antes del turno: cada comando de agente recarga la
// sesión de la BD, así que lo que solo viviera en memoria se perdería para el siguiente (el número
// de intento volvería a 1 y el rechazo tipado no llegaría a quien lo lea en otro turno).
func (p *MessageWorkerPool) reescalateRejectedAgentInput(ctx context.Context, sess *session.Session,
	cmd AgentCommand, step string, res statemachine.ReturnInputResult,
) {
	n := 1
	if v := sess.GetContext(statemachine.CtxAgentInputRejected); v != "" {
		if prev, err := strconv.Atoi(v); err == nil {
			n = prev + 1
		}
	}

	// Un dato del agente que el bot no pudo usar es un fallo medible del lado humano: si sube, lo
	// que hay que revisar es la pantalla del agente o el texto de ayuda, no el bot.
	if p.tracker != nil {
		p.tracker.LogEvent(ctx, sess.ID, cmd.Phone, "agent_input_rejected", map[string]interface{}{
			"step":    step,
			"attempt": n,
			"problem": res.Problem,
		})
	}
	observability.Emit(observability.TraceSession(sess.ID), "escalacion", "agent_input_rejected",
		observability.EmitOpts{Phone: sess.PhoneNumber, Reason: step})
	// ⚠️ NI EL MOTIVO NI EL VALOR SE TRAZAN: el valor es lo que una persona escribió sobre un
	// paciente —en ASK_DOCUMENT es su documento— y el motivo puede ecoarlo. Los dos van a la nota
	// que lee el agente y al rechazo tipado; aquí solo lo que hace falta para ENCONTRAR el caso.
	slog.Warn("agent return: el dato escrito por el agente no se pudo usar",
		"phone", utils.MaskPhone(cmd.Phone), "session_id", sess.ID, "step", step,
		"attempt", n)

	updateCtx := map[string]string{
		statemachine.CtxAgentInputRejected: strconv.Itoa(n),
		"escalation_note":                  statemachine.RejectedInputNote(n, truncateRejectedEcho(res.Value), res.Problem),
		// El paso bloqueado tiene que seguir siendo el de origen: es de donde sale el menú de
		// retornos que el agente va a volver a ver. Lo normal es que lo ponga el auto-chain de la
		// máquina; aquí se entra por fuera, así que se pone a mano.
		statemachine.CtxPreAutoState: step,
	}
	// El rechazo TIPADO, para el sobre v1: lo escribe quien re-escala y lo leerá, en OTRO turno,
	// el handler de escalación al armar el sobre. Si no se pudiera rendir, el caso escala igual y
	// solo se pierde la clave tipada; la nota, que es lo que el agente LEE, no depende de esto.
	if raw, err := statemachine.EncodeRejection(statemachine.PersistedRejection{
		Reason:  res.Problem,
		Attempt: n,
		Echo:    truncateRejectedEcho(res.Value),
		Field:   res.Field,
		Piece:   res.Piece,
	}); err != nil {
		slog.Error("no se pudo rendir el rechazo tipado, la escalación sale sin `rechazo`",
			"session_id", sess.ID, "step", step)
	} else {
		updateCtx[statemachine.CtxAgentInputRejection] = raw
	}

	if err := p.sessionManager.ResumeFromEscalation(ctx, sess, statemachine.StateEscalateToAgent); err != nil {
		slog.Error("reescalada por dato rechazado: no se pudo retomar la sesión", "error", err,
			"phone", utils.MaskPhone(cmd.Phone), "session_id", sess.ID)
		return
	}
	// La espera de esta escalación se acabó: un agente tomó el caso y escribió. La marca se borra
	// en la MISMA escritura, antes del turno de abajo, que vuelve a escalar y decide de nuevo si
	// corresponde.
	var clearCtx []string
	if sess.GetContext(statemachine.CtxEscalationNextDay) != "" {
		clearCtx = []string{statemachine.CtxEscalationNextDay}
	}
	if err := p.sessionManager.SaveState(ctx, sess, statemachine.StateEscalateToAgent, updateCtx, clearCtx); err != nil {
		// El turno sigue: la escalación de abajo usa la sesión en memoria. Lo que se pierde es el
		// contexto para los turnos siguientes (intento y rechazo tipado).
		slog.Error("reescalada por dato rechazado: no se pudo persistir el contexto", "error", err,
			"phone", utils.MaskPhone(cmd.Phone), "session_id", sess.ID)
		for k, v := range updateCtx {
			sess.SetContext(k, v)
		}
	}
	_ = p.birdClient.UnassignFeedItem(sess.PhoneNumber, sess.ConversationID, false)

	// El texto NO puede ser __resume__: ese es el cortacircuito anti-bucle del handler de
	// escalación y suprimiría justo la escalación que hace falta.
	virtualMsg := bird.InboundMessage{
		ID:          fmt.Sprintf("agent-input-rejected-%s-%d", cmd.Phone, time.Now().UnixNano()),
		Phone:       sess.PhoneNumber,
		Text:        "__agent_input_rejected__",
		MessageType: "text",
		ReceivedAt:  time.Now(),
	}
	result, err := p.machine.Process(ctx, sess, virtualMsg)
	if err != nil {
		slog.Error("reescalada por dato rechazado: falló el proceso", "error", err,
			"phone", utils.MaskPhone(cmd.Phone), "session_id", sess.ID)
		return
	}
	if result != nil {
		p.sendAndSave(ctx, sess, sess.PhoneNumber, result)
	}
}
