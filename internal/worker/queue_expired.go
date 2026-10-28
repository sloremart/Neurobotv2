package worker

import (
	"context"
	"errors"
	"log/slog"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/nectar"
	"github.com/neuro-bot/neuro-bot/internal/observability"
	"github.com/neuro-bot/neuro-bot/internal/session"
	"github.com/neuro-bot/neuro-bot/internal/statemachine"
	"github.com/neuro-bot/neuro-bot/internal/utils"
)

// queueExpiredTemplateProject es el ProjectID con el que se pide la PLANTILLA del aviso de cola
// vencida. Con Néctar, el puente traduce ProjectID → nombre de plantilla de Meta con la tabla de
// NECTAR_TEMPLATE_NAMES (internal/nectar/bridge.go), así que basta con que el operador añada la
// entrada `cola_vencida=<nombre_en_meta>`; mientras no exista, SendTemplate devuelve el error que
// dice exactamente qué falta y el caso se cierra igual, sin aviso.
const queueExpiredTemplateProject = "cola_vencida"

// templateSender es la parte del cliente de mensajería que manda plantillas aprobadas. No está en
// MessageSender —el contrato del envío corriente— porque solo este camino la usa: fuera de la
// ventana de 24 h de WhatsApp el texto libre no sale. Se pide por aserción de tipo (el
// `*bird.Client` real la cumple) para no obligar a todos los dobles de prueba del pool a
// implementarla.
type templateSender interface {
	SendTemplate(phone string, tmpl bird.TemplateConfig) (string, error)
}

// SetBusinessHoursText cablea el horario de atención EN PALABRAS, que es lo que el bot le dice
// al paciente cuando lo manda a volver. Es una función y no un texto porque el horario se
// PREGUNTA en cada aviso y puede cambiar sin redespliegue. Sin cablear, el aviso nombra «nuestro
// horario de atención».
func (p *MessageWorkerPool) SetBusinessHoursText(f func() string) {
	p.businessHoursText = f
}

// businessHoursTextValue describe el horario vigente en el instante de escribirle al paciente.
func (p *MessageWorkerPool) businessHoursTextValue() string {
	if p.businessHoursText == nil {
		return ""
	}
	return p.businessHoursText()
}

// queueExpiredText es lo que lee el paciente cuyo caso pasó en la cola de Néctar el tiempo
// máximo sin que ningún asesor lo tomara: se le DICE que nadie alcanzó a atenderlo y cuándo
// volver, en vez de cerrarle el chat en silencio.
func queueExpiredText(hours string) string {
	if hours == "" {
		return "No alcanzamos a atenderte a tiempo 😔. Escríbenos de nuevo en nuestro horario de atención."
	}
	return "No alcanzamos a atenderte a tiempo 😔. Escríbenos de nuevo en nuestro horario: " + hours + "."
}

// handleQueueExpired atiende el retorno `cola_vencida` (statemachine.ReturnValueQueueExpired):
// el barredor de Néctar devolvió al bot una conversación que pasó en `waiting` el tiempo máximo
// sin asesor.
//
// El aviso lo da el BOT y no Néctar porque Néctar no tiene remitente de sistema hacia el
// paciente; y la conversación vuelve al bot ANTES de resolverse porque el bot no puede escribirle
// a una que no es suya. De ahí el orden: primero se avisa —con la conversación ya nuestra—,
// después se completa la sesión y se cierra.
//
// El cierre NO es condicional al aviso: que el aviso falle (ventana de 24 h, canal caído) no
// puede dejar la sesión escalada para siempre, que es justo el limbo del que este camino la saca.
// El siguiente mensaje del paciente abre conversación nueva, como siempre.
func (p *MessageWorkerPool) handleQueueExpired(ctx context.Context, sess *session.Session) {
	// Guarda de idempotencia: el aviso puede llegar dos veces, o un asesor puede haber tomado el
	// caso entre el barrido y este turno. Info y no Warn: no es un fallo de nadie.
	if sess.Status != session.StatusEscalated {
		slog.Info("cola vencida sobre una sesión que ya no está escalada, se ignora",
			"session_id", sess.ID, "phone", utils.MaskPhone(sess.PhoneNumber), "status", sess.Status)
		return
	}

	p.notifyQueueExpired(sess, p.businessHoursTextValue())

	completeErr := p.sessionManager.Complete(ctx, sess)
	if completeErr != nil {
		slog.Error("cola vencida: no se pudo completar la sesión", "error", completeErr,
			"session_id", sess.ID, "phone", utils.MaskPhone(sess.PhoneNumber))
	} else {
		// La espera se acabó y la marca no puede sobrevivir a una sesión completada. Mismo
		// criterio que handleAgentClose.
		p.clearNextDayQueueMark(ctx, sess)
	}

	// Cierre SÍNCRONO: con Néctar el envío de arriba es un POST que ya volvió con el id del
	// mensaje, así que no hay nada en vuelo que el cierre pueda cortar. Se intenta aunque Complete
	// haya fallado: la conversación vencida no puede quedarse abierta en Néctar sin dueño.
	if sess.ConversationID != "" {
		if err := p.birdClient.CloseFeedItems(sess.PhoneNumber, sess.ConversationID); err != nil {
			slog.Warn("cola vencida: no se pudo cerrar la conversación",
				"conversation_id", sess.ConversationID, "session_id", sess.ID, "error", err,
				"complete_error", completeErr)
		}
	}
	if completeErr != nil {
		return
	}

	if p.tracker != nil {
		p.tracker.LogEvent(ctx, sess.ID, sess.PhoneNumber, "session_completed", nil)
		p.tracker.LogEvent(ctx, sess.ID, sess.PhoneNumber, "escalation_closed", nil)
	}
	// cola_vencida cuenta cuántos pacientes esperaron sin que nadie los tomara: la promesa
	// incumplida que Néctar detecta y el bot confiesa.
	observability.Emit(observability.TraceSession(sess.ID), "escalacion", "cola_vencida",
		observability.EmitOpts{Phone: sess.PhoneNumber, Reason: statemachine.ReturnValueQueueExpired})
	slog.Info("cola vencida: caso cerrado por el bot tras la devolución de Néctar",
		"session_id", sess.ID, "phone", utils.MaskPhone(sess.PhoneNumber))
}

// notifyQueueExpired le dice al paciente que nadie alcanzó a atenderlo. Texto libre mientras la
// ventana de 24 h de WhatsApp siga abierta; con la ventana cerrada (409 window_closed →
// nectar.ErrWindowClosed) solo sale una plantilla aprobada.
//
// No hay re-escalación de respaldo: devolver a la cola el caso que acaba de vencer EN la cola es
// justo lo que no se puede hacer. Si tampoco sale la plantilla, queda el aviso en el log y el
// caso se cierra.
func (p *MessageWorkerPool) notifyQueueExpired(sess *session.Session, hours string) {
	_, err := p.birdClient.SendText(sess.PhoneNumber, sess.ConversationID, queueExpiredText(hours))
	if err == nil {
		return
	}
	if !errors.Is(err, nectar.ErrWindowClosed) {
		slog.Warn("cola vencida: el aviso al paciente no salió", "error", err,
			"session_id", sess.ID, "phone", utils.MaskPhone(sess.PhoneNumber))
		return
	}
	ts, ok := p.birdClient.(templateSender)
	if !ok {
		slog.Warn("cola vencida: ventana de 24 h cerrada y el canal no manda plantillas",
			"session_id", sess.ID, "phone", utils.MaskPhone(sess.PhoneNumber))
		return
	}
	// La plantilla lleva el horario como parámetro y Meta rechaza un parámetro vacío: sin horario
	// cableado no se gasta el envío. El caso se cierra igual.
	if hours == "" {
		slog.Error("cola vencida: ventana de 24 h cerrada y sin horario para la plantilla; el caso se cierra sin avisar",
			"session_id", sess.ID, "phone", utils.MaskPhone(sess.PhoneNumber))
		return
	}
	tmpl := bird.TemplateConfig{
		ProjectID: queueExpiredTemplateProject,
		Params: []bird.TemplateParam{
			{Type: "string", Key: "horario", Value: hours},
		},
	}
	if _, err := ts.SendTemplate(sess.PhoneNumber, tmpl); err != nil {
		slog.Warn("cola vencida: la plantilla tampoco salió; el caso se cierra sin avisar",
			"error", err, "session_id", sess.ID, "phone", utils.MaskPhone(sess.PhoneNumber))
		return
	}
	slog.Info("cola vencida: aviso enviado por plantilla (ventana de 24 h cerrada)",
		"session_id", sess.ID, "phone", utils.MaskPhone(sess.PhoneNumber))
}

// SetEscalationQueuedNextDay cablea el predicado que dice si la escalación de una sesión de Néctar
// se encola para el próximo día hábil (handlers.NextDayQueueApplies). Con él, PreEscalationFlush
// descarta el aviso del caller y el único aviso al paciente es el del próximo día. Sin cablear, el
// aviso del caller sale siempre.
func (p *MessageWorkerPool) SetEscalationQueuedNextDay(f func(*session.Session) bool) {
	p.escalationQueuedNextDay = f
}
