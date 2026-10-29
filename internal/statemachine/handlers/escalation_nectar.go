package handlers

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/config"
	"github.com/neuro-bot/neuro-bot/internal/observability"
	"github.com/neuro-bot/neuro-bot/internal/session"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
)

// Piezas de la escalación que existen por Néctar: el sobre del escalado (v0 / v1), el equipo
// sugerido por grupo de CUPS, la cola del próximo día hábil y el aviso al paciente. El handler
// (escalation.go) las usa; el camino de Bird no depende de ninguna salvo de escalationExit.

// gateOutOfHours es el gate que corta la escalación por el RELOJ (el horario cerrado, o la
// ventana con la apertura demasiado lejos), frente a "no_agents", que la corta porque no hay
// nadie conectado. Es literal de flow_events.
const gateOutOfHours = "out_of_hours"

// queuedExpiryExtra es cuánto vive la sesión DESPUÉS de la apertura que termina la espera de una
// escalación encolada: el plazo tiene que cubrir el tiempo de ATENCIÓN, no solo la espera.
const queuedExpiryExtra = 8 * time.Hour

// emitSuggestedTeam deja `escalacion/equipo_sugerido` DESPUÉS de que Néctar aceptó el escalado:
// emitirlo al armar el sobre contaría equipos sugeridos de casos que Néctar nunca recibió. Solo
// cuando hay algo que sugerir: un evento por cada escalación sin mapeo no diría nada.
func emitSuggestedTeam(cfg *config.Config, sess *session.Session, team *int64) {
	if team == nil || sess == nil {
		return
	}
	observability.Emit(observability.TraceSession(sess.ID), "escalacion", "equipo_sugerido",
		observability.EmitOpts{Phone: sess.PhoneNumber, Attrs: map[string]interface{}{
			"grupo": cfg.CupsGroup(sess.GetContext("cups_code")), "team_id": *team,
		}})
}

// sendAgentCommandsNote manda al Inbox de Bird la nota con los comandos para el agente (invisible
// para el paciente). Es variable para que las pruebas cuenten las llamadas.
var sendAgentCommandsNote = func(c *bird.Client, phone, conversationID, commands string) {
	_, _ = c.SendInternalText(phone, conversationID, commands)
}

// escalationEnvelope arma el sobre del escalado con la forma que la BANDERA manda
// (PROTOCOL_ESCALADO_VERSION).
//
// CON LA BANDERA EN 0 el sobre sale como el dialecto de siempre: `protocol` queda en 0 (no
// viaja), `reason_detail` ni se calcula y lo que se le pinta al agente es el menú de retornos.
// Con la bandera en 1 entran las claves de v1: el detalle del motivo, los hechos (`facts`), el
// descriptor de entrada (`input`, que SUSTITUYE al menú) y el rechazo tipado. Las dos formas se
// arman aquí, del mismo paso y la misma sesión, para que la exclusión menú/descriptor sea
// estructural.
//
// El EQUIPO SUGERIDO va en los dos dialectos (el campo existe en Néctar desde antes de v1): sale
// del grupo del CUPS de la sesión (NECTAR_TEAM_BY_GROUP). Sin mapa es nil y no viaja.
//
// cfg puede ser nil en pruebas que no la cablean: ahí el sobre es el del dialecto.
func escalationEnvelope(cfg *config.Config, m *sm.Machine, sess *session.Session,
	step, reason, summary string, facts []*summaryGroup, resolveLock string,
) bird.Escalation {
	esc := bird.Escalation{
		Reason:          reason,
		Summary:         summary,
		ResolveLock:     resolveLock,
		SuggestedTeamID: cfg.SuggestedTeamForCups(sess.GetContext("cups_code")),
	}
	if cfg == nil || !cfg.EmitsProtocolV1() {
		// Menú de retornos: las salidas válidas DESDE ESTE PASO. Si no se puede serializar se
		// escala sin él: quedarse sin menú degrada la vuelta, fallar aquí abandona al paciente.
		if raw, err := json.Marshal(m.ReturnMenu(sess, step)); err != nil {
			slog.Error("escalación: menú de retornos no serializable, se escala sin él",
				"error", err, "session_id", sess.ID, "step", step)
		} else {
			esc.ReturnMenu = raw
		}
		return esc
	}
	esc.Protocol = config.ProtocolEscaladoV1
	// El detalle es el escalation_reason INTERNO del bot. Vacío no viaja (omitempty en el cable).
	esc.ReasonDetail = sess.GetContext(sm.CtxEscalationReason)
	// Los hechos ACOMPAÑAN al resumen, no lo sustituyen. Sin serializar se escala sin ellos.
	if raw, err := json.Marshal(facts); err != nil {
		slog.Error("escalación: los hechos del resumen no se pudieron serializar, se escala sin ellos",
			"error", err, "session_id", sess.ID)
	} else {
		esc.Facts = raw
	}
	// El descriptor de entrada: lo que Néctar le pinta al agente para devolver la conversación.
	if raw, err := json.Marshal(m.InputDescriptor(sess, step)); err != nil {
		slog.Error("escalación: el descriptor de entrada no se pudo serializar, se escala sin él",
			"error", err, "session_id", sess.ID, "step", step)
	} else {
		esc.Input = raw
	}
	// El RECHAZO TIPADO: sale SOLO en la re-escalada que lo escribió el worker (un dato del
	// agente que no sirvió). Es aditivo: la nota del resumen no cambia.
	if rej, ok := sm.DecodeRejection(sess.GetContext(sm.CtxAgentInputRejection)); ok {
		esc.Rejection = &bird.Rejection{
			Detail:  rej.Reason,
			Attempt: rej.Attempt,
			Echo:    rej.Echo,
			Field:   rej.Field,
			Piece:   rej.Piece,
		}
	}
	return esc
}

// escalationExit es LA SALIDA del paso de escalación: todos los `return` del handler pasan por
// ella, y por eso es donde vive lo que hay que dejar resuelto se escale o no.
//
// (1) EL RECHAZO TIPADO se borra en TODAS las salidas: el paso puede terminar sin escalar (tope,
// desvío, fallo de canal) y una clave viva emitiría en la siguiente escalación un rechazo que no
// es el de su caso.
//
// (2) LA MARCA «EL PACIENTE PIDIÓ LA PERSONA» también: describe ESTA escalación. Si el paso
// termina sin escalar, una marca viva haría que la siguiente —una en que el bot se atasca—
// saliera como pedida por el paciente (motivo del sobre y aviso de Néctar equivocados). Si el
// paciente vuelve a pedir un asesor, el interceptor o el menú la escriben de nuevo.
//
// (3) EL MOTIVO INTERNO (CtxEscalationReason) también: describe ESTA escalación. El evento
// `escalated` y el sobre lo leen de la sesión ANTES de que esta salida se aplique; una clave viva
// rotularía la siguiente escalación, que escribe el suyo, con el motivo de la anterior.
//
// (4) LO ÚLTIMO QUE ESCRIBIÓ EL PACIENTE y la lista de espera atada al caso (CtxEscalationLastInput,
// CtxEscalationWaitingListID) igual, y también lo que dejó la lectura de la orden
// (CtxOCRFailureCause, CtxOCRUnreadNames): son datos de ESTA escalación y el resumen los pinta
// tal cual. Regla: toda clave que describe UNA escalación se borra al salir.
func escalationExit(r *sm.StateResult) *sm.StateResult {
	if r == nil {
		return r
	}
	return r.WithClearCtx(sm.CtxAgentInputRejection, sm.CtxPatientAskedForAgent, sm.CtxEscalationReason,
		sm.CtxEscalationLastInput, sm.CtxEscalationWaitingListID, sm.CtxOCRFailureCause, sm.CtxOCRUnreadNames)
}

// reasonNotifiesPatient dice si el motivo interno obliga a avisarle al paciente de Néctar aunque
// no lo haya pedido: son las escalaciones cuyo caller no trae texto para él (la entidad «particular
// liga» y el kill switch), y sin aviso el paciente se quedaría en silencio esperando.
func reasonNotifiesPatient(reason string) bool {
	return reason == sm.EscalationReasonParticularLiga || reason == sm.EscalationReasonBotDisabled
}

// queuedWaitStillOpen dice si la escalación ENCOLADA de esta sesión sigue DENTRO de su espera:
// desde que se encoló hasta la apertura que la termina. Es el espejo, del lado del handler, de
// la exención del checker de sesiones (session.escalationExempt con InactivityDeps.NextOpen).
//
// La exención caduca: una marca que no expira dejaría el cortacircuito anti-bucle apagado para
// el resto de la sesión. Sin sello de escalación se comporta como exenta (el lado seguro).
func queuedWaitStillOpen(sess *session.Session) bool {
	if sess.GetContext(sm.CtxEscalationNextDay) != "1" {
		return false
	}
	if sess.EscalatedAt == nil {
		return true
	}
	return nowFunc().Before(QueuedWaitEnd(sess.EscalatedAt.In(nowFunc().Location())))
}

// queuedExpiryUntil es hasta cuándo tiene que vivir la sesión de una escalación encolada en
// `now`: la apertura que termina la espera más queuedExpiryExtra.
func queuedExpiryUntil(now time.Time) time.Time {
	return QueuedWaitEnd(now).Add(queuedExpiryExtra)
}

// escalationNoticeText es lo que el paciente de Néctar lee cuando su caso SÍ se escala. Hay dos:
// el de siempre, cuando un asesor puede tomarlo ahora, y el del PRÓXIMO DÍA HÁBIL, cuando el
// caso entra en la ventana de cierre o con el horario cerrado — prometer «un momento por favor»
// a las 18:30 sería mentirle a quien va a esperar hasta mañana. «y te escribirá por aquí» dice
// que no tiene que volver a escribir para que lo atiendan.
func escalationNoticeText(nextDay bool) string {
	if !nextDay {
		return sm.EscalationNoticeText
	}
	return fmt.Sprintf("Tu solicitud quedó registrada ✅. "+
		"Un asesor te atenderá el próximo día hábil y te escribirá por aquí. "+
		"Te atendemos %s", BusinessHoursDescription())
}

// NextDayQueueApplies devuelve el predicado que el pre-envío del pool consulta antes del escalate:
// true si la escalación de ESA sesión se va a encolar para el próximo día hábil, con el mismo
// criterio que el handler (solo Néctar, nextBusinessDayQueue). Así el aviso genérico de conexión
// («te voy a conectar… un momento») no sale antes del aviso del próximo día; el resto del turno sí.
func NextDayQueueApplies(cfg *config.Config) func(*session.Session) bool {
	return func(sess *session.Session) bool {
		return sess != nil && session.IsNectarSession(sess.PhoneNumber) && nextBusinessDayQueue(cfg).queue
	}
}
