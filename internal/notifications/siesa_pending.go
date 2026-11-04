package notifications

import (
	"context"
	"log/slog"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/config"
	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/observability"
	"github.com/neuro-bot/neuro-bot/internal/session"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
	"github.com/neuro-bot/neuro-bot/internal/utils"
)

// Respuesta al recordatorio pendiente de SIESA (D-04). Si el paciente pulsa «Confirmar» o
// «Cancelar» y SIESA no responde, la respuesta queda marcada en su fila de notification_history
// (migración 047) y un proceso de fondo la retoma cuando SIESA vuelve: la confirmación se aplica;
// la cancelación NO, se le pide al paciente que la confirme, como al pulsar «Cancelar».

// Acciones que pueden quedar pendientes de SIESA (columna siesa_pending).
const (
	siesaActionConfirm = "confirm"
	siesaActionCancel  = "cancel"
)

// Desenlaces de una respuesta que deja de estar pendiente (columna siesa_outcome).
const (
	siesaOutcomeApplied        = "applied"         // el proceso de fondo confirmó en SIESA
	siesaOutcomeCancelPrompted = "cancel_prompted" // se abrió la confirmación de la cancelación
	siesaOutcomeAlreadyApplied = "already_applied" // la cita ya estaba como pidió el paciente
	siesaOutcomeNotApplicable  = "not_applicable"  // la cita cambió: no se tocó y se escaló
	siesaOutcomeUnattended     = "unattended"      // venció un tope con el paciente conversando en Néctar: sin escalar, ver evento
	siesaOutcomeGaveUp         = "gave_up"         // venció un tope: se escaló
)

// Textos al paciente cuando su respuesta queda pendiente de SIESA.
const (
	siesaConfirmDeferredText = "Recibimos tu confirmación. La registraremos en unos minutos."
	siesaCancelDeferredText  = "Recibimos tu solicitud de cancelación. Te escribiremos en unos minutos para confirmarla."
)

// Topes de la espera: pasado cualquiera de ellos el bot deja de reintentar y escala a un agente.
const (
	// siesaPendingMaxAge: una respuesta que lleva más de esto pendiente ya no se aplica sola. Queda
	// dentro de la ventana de 24 h de WhatsApp que abrió la respuesta del paciente.
	siesaPendingMaxAge = 6 * time.Hour
	// siesaPendingMinLead: con la cita a menos de esto, la atiende una persona.
	siesaPendingMinLead = 2 * time.Hour
)

// SiesaPendingRow es una respuesta al recordatorio pendiente de aplicar en SIESA.
type SiesaPendingRow struct {
	ID            int64 // id de la fila de notification_history
	Phone         string
	Type          string
	AppointmentID string
	// AppointmentSlot: fecha y hora (TimeSlot) de la cita recordada; vacía = no se compara el horario.
	AppointmentSlot string
	BirdMessageID   string
	ConversationID  string
	ContactPhone    string
	Action          string        // "confirm" | "cancel"
	Attempts        int           // intentos fallidos del proceso de fondo
	PendingFor      time.Duration // tiempo desde que quedó pendiente (medido en la BD)
}

// SiesaPendingStore guarda las respuestas pendientes de SIESA (implementado por local.NotificationRepo).
type SiesaPendingStore interface {
	// MarkSiesaPending marca como pendiente la última fila del historial de ese teléfono y cita, con
	// la conversación si llega; si no hay fila (el Resolve falló) inserta una con status.
	MarkSiesaPending(ctx context.Context, row SiesaPendingRow, status string) error
	// FindSiesaPending devuelve hasta limit respuestas pendientes, la más vieja primero.
	FindSiesaPending(ctx context.Context, limit int) ([]SiesaPendingRow, error)
	// RecordSiesaAttempt suma un intento fallido a una respuesta que sigue pendiente.
	RecordSiesaAttempt(ctx context.Context, id int64) error
	// FinishSiesaPending saca la respuesta de pendientes con su desenlace. Devuelve false si ya no
	// estaba pendiente.
	FinishSiesaPending(ctx context.Context, id int64, outcome string) (bool, error)
}

// SetSiesaPendingStore inyecta dónde quedan las respuestas pendientes de SIESA. Sin él (nil) un
// fallo de SIESA en la respuesta se trata como antes.
func (m *NotificationManager) SetSiesaPendingStore(s SiesaPendingStore) {
	m.siesaPending = s
}

// deferToSiesa deja pendiente de SIESA la respuesta del paciente (action) a la que SIESA no
// respondió (cause) y se lo dice al paciente. Devuelve false si no la pudo guardar (sin store o con
// la BD local caída): entonces no se promete nada y el llamador sigue con el trato de antes.
// appt es la cita si se alcanzó a leer (falló la escritura): da la fecha y hora cuando el pending no
// las trae.
func (m *NotificationManager) deferToSiesa(ctx context.Context, phone, action string, pending *PendingNotification, appt *domain.Appointment, cause error) bool {
	if m.siesaPending == nil {
		return false
	}
	// El ctx de la respuesta puede venir gastado por la propia SIESA colgada: la marca va con uno nuevo.
	markCtx, cancel := utils.MarkContext(ctx)
	defer cancel()
	row := SiesaPendingRow{
		Phone: phone, Type: pending.Type, AppointmentID: pending.AppointmentID, AppointmentSlot: pending.AppointmentSlot,
		BirdMessageID: pending.BirdMessageID, ConversationID: pending.ConversationID, ContactPhone: pending.ContactPhone,
		Action: action,
	}
	if row.AppointmentSlot == "" && appt != nil {
		row.AppointmentSlot = appt.TimeSlot
	}
	if err := m.siesaPending.MarkSiesaPending(markCtx, row, responseStatus(action)); err != nil {
		slog.Error("respuesta al recordatorio: SIESA no respondió y la respuesta no quedó pendiente",
			"phone", utils.MaskPhone(phone), "appointment_id", pending.AppointmentID, "action", action,
			"siesa_error", cause, "error", err)
		return false
	}
	text := siesaConfirmDeferredText
	if action == siesaActionCancel {
		text = siesaCancelDeferredText
	}
	_, _ = m.birdClient.SendText(phone, pending.ConversationID, text) //nolint:contextcheck // cliente Bird sin ctx (patrón existente)
	observability.Emit(observability.TraceNotif(pending.AppointmentID), "notif_recordatorio", "siesa_pending",
		observability.EmitOpts{Phone: phone, Reason: action})
	slog.Warn("respuesta al recordatorio pendiente de SIESA",
		"phone", utils.MaskPhone(phone), "appointment_id", pending.AppointmentID, "action", action, "error", cause)
	return true
}

// siesaPendingBatch acota las filas de una pasada: las que sobren van en la siguiente.
const siesaPendingBatch = 50

// StartSiesaPendingRetrier retoma, cada cfg.SiesaPendingRetryMin minutos, las respuestas al
// recordatorio que quedaron pendientes de SIESA. Las pasadas son secuenciales (un solo bucle): dos
// pasadas nunca trabajan la misma fila a la vez.
func (m *NotificationManager) StartSiesaPendingRetrier(ctx context.Context) {
	if m.siesaPending == nil {
		return
	}
	every := config.DefaultSiesaPendingRetryMin
	if m.cfg != nil && m.cfg.SiesaPendingRetryMin > 0 {
		every = m.cfg.SiesaPendingRetryMin
	}
	ticker := time.NewTicker(time.Duration(every) * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.runSiesaPendingPass(ctx)
		}
	}
}

// runSiesaPendingPass es UNA pasada, con plazo propio (D-07).
func (m *NotificationManager) runSiesaPendingPass(ctx context.Context) {
	utils.RunPass(ctx, "siesa-pending-retry", m.passTimeout, func(passCtx context.Context) {
		m.retrySiesaPending(passCtx, time.Now())
	})
}

// retrySiesaPending recorre las respuestas pendientes de SIESA, cada una con el ctx de su fila.
func (m *NotificationManager) retrySiesaPending(ctx context.Context, now time.Time) {
	rows, err := m.siesaPending.FindSiesaPending(ctx, siesaPendingBatch)
	if err != nil {
		if ctx.Err() == nil { // pasada vencida: el error ya lo deja RunPass
			slog.Error("respuestas pendientes de SIESA: listar", "error", err)
		}
		return
	}
	for _, row := range rows {
		if ctx.Err() != nil {
			return // pasada vencida o apagado: las que faltan, en la pasada siguiente
		}
		rowCtx, cancel := utils.RowContext(ctx)
		m.retrySiesaRow(rowCtx, row, now)
		cancel()
	}
}

// retrySiesaRow retoma UNA respuesta pendiente. Releer la cita antes de escribir hace de SIESA la
// llave de idempotencia: si una pasada aplicó y envió pero no pudo cerrar la fila, la siguiente
// encuentra la cita ya como pidió el paciente y la cierra sin reenviar nada. Lo mismo vale si
// ConfirmBlock escribió pero devolvió error (escritura en duda) o si la confirmó otro: se cierra como
// already_applied y el paciente se queda con el aviso «la registraremos», sin el detalle de la cita.
func (m *NotificationManager) retrySiesaRow(ctx context.Context, row SiesaPendingRow, now time.Time) {
	if !m.lockPhone(row.Phone) { //nolint:contextcheck // el candado del manager acota su propia espera
		return // la pasada siguiente la retoma
	}
	defer m.unlockPhone(row.Phone)

	pending := row.notification()
	expired := row.PendingFor > siesaPendingMaxAge
	appt, block, err := m.apptSvc.FindBlockByAppointmentID(ctx, row.AppointmentID)
	if err != nil {
		if expired {
			m.handOverSiesaRow(ctx, row, pending, siesaOutcomeGaveUp, true, now)
			return
		}
		m.recordSiesaFailure(ctx, row, err)
		return
	}
	// Con la cita leída, una ya resuelta se cierra en silencio aunque haya vencido el tope.
	switch siesaCheck(row, appt) {
	case siesaOutcomeAlreadyApplied:
		m.finishSiesaRow(ctx, row, siesaOutcomeAlreadyApplied)
		return
	case siesaOutcomeNotApplicable:
		capped := expired || (appt != nil && startsWithin(appt, now, siesaPendingMinLead))
		m.handOverSiesaRow(ctx, row, pending, siesaOutcomeNotApplicable, capped, now)
		return
	}
	if expired || startsWithin(appt, now, siesaPendingMinLead) {
		m.handOverSiesaRow(ctx, row, pending, siesaOutcomeGaveUp, true, now)
		return
	}

	if row.Action == siesaActionCancel {
		m.promptDeferredCancel(ctx, row, pending, appt, block, now)
		return
	}

	if err := m.apptSvc.ConfirmBlock(ctx, block, "whatsapp_bot", row.ConversationID); err != nil {
		m.recordSiesaFailure(ctx, row, err)
		return
	}
	_, _ = m.birdClient.SendText(row.Phone, row.ConversationID, m.buildConfirmationMessage(ctx, appt, block)) //nolint:contextcheck // cliente Bird sin ctx (patrón existente)

	markCtx, cancel := utils.MarkContext(ctx)
	defer cancel()
	m.finishSiesaRow(markCtx, row, siesaOutcomeApplied)
	if m.tracker != nil {
		m.tracker.LogEvent(markCtx, "", row.Phone, "notification_confirmed", map[string]interface{}{
			"appointment_id":  row.AppointmentID,
			"total_confirmed": len(block),
			"conversation_id": row.ConversationID,
			"siesa_deferred":  true,
		})
	}
	observability.Emit(observability.TraceNotif(row.AppointmentID), "notif_recordatorio", "confirmed",
		observability.EmitOpts{Phone: row.Phone, Reason: "whatsapp_siesa_deferred"})
	slog.Info("respuesta pendiente de SIESA aplicada",
		"phone", utils.MaskPhone(row.Phone), "appointment_id", row.AppointmentID, "action", row.Action,
		"attempts", row.Attempts, "total", len(block))
}

// promptDeferredCancel abre la confirmación de la cancelación, la misma que al pulsar «Cancelar»
// con SIESA arriba: el bot NO cancela; la cancelación (y el aviso a la lista de espera) ocurre solo
// si el paciente la confirma, por el camino de siempre. Si el paciente está en otra conversación con
// el bot no se la interrumpe: la fila sigue pendiente y se vuelve a mirar en la pasada siguiente,
// hasta que esa conversación termine o venza un tope. SIESA ya se leyó (appt, block): entre la
// comprobación de la sesión y su creación solo hay escrituras en la BD local.
//
// La fila se cierra al encolar el mensaje virtual que hace la pregunta: si el pool lo descarta por
// contrapresión o se reinicia antes de enviarlo, queda el error en el log y la sesión abierta sin
// pregunta, igual que en el camino inmediato.
func (m *NotificationManager) promptDeferredCancel(ctx context.Context, row SiesaPendingRow, pending *PendingNotification, appt *domain.Appointment, block []domain.Appointment, now time.Time) {
	if m.sessionRepo == nil || m.workerPool == nil {
		slog.Error("respuesta pendiente de SIESA: sin sesiones para pedir la confirmación de la cancelación")
		m.handOverSiesaRow(ctx, row, pending, siesaOutcomeGaveUp, true, now)
		return
	}
	own, busy, err := m.patientSession(ctx, row, now)
	switch {
	case err != nil:
		m.recordSiesaFailure(ctx, row, err)
		return
	case own:
		// Una pasada anterior ya abrió la pregunta y no pudo cerrar la fila: no se reabre ni se escala.
		m.finishSiesaRow(ctx, row, siesaOutcomeCancelPrompted)
		return
	case busy:
		waitForPatient(row)
		return
	}
	if err := m.openConfirmCancelSession(ctx, row.Phone, pending, appt, block); err != nil {
		m.recordSiesaFailure(ctx, row, err)
		return
	}

	markCtx, cancel := utils.MarkContext(ctx)
	defer cancel()
	m.finishSiesaRow(markCtx, row, siesaOutcomeCancelPrompted)
	observability.Emit(observability.TraceNotif(row.AppointmentID), "notif_recordatorio", "siesa_cancel_prompted",
		observability.EmitOpts{Phone: row.Phone})
	slog.Info("respuesta pendiente de SIESA: se pidió confirmar la cancelación",
		"phone", utils.MaskPhone(row.Phone), "appointment_id", row.AppointmentID, "attempts", row.Attempts)
}

// patientSession mira la sesión que ocupa el teléfono de la fila: own = es la pregunta de
// cancelación de ESTA cita (isOwnCancelSession); busy = es una conversación en curso (sessionInUse),
// sea cual sea. Sin repositorio de sesiones no hay conversación que respetar.
func (m *NotificationManager) patientSession(ctx context.Context, row SiesaPendingRow, now time.Time) (own, busy bool, err error) {
	if m.sessionRepo == nil {
		return false, false, nil
	}
	current, err := m.sessionRepo.FindCurrentByPhone(ctx, row.Phone)
	if err != nil {
		return false, false, err
	}
	own, err = m.isOwnCancelSession(ctx, current, row.AppointmentID)
	return own, sessionInUse(current, now), err
}

// waitForPatient deja la fila pendiente, sin sumar intento: el paciente está conversando con el bot
// o con un agente, y la pasada siguiente la vuelve a mirar.
func waitForPatient(row SiesaPendingRow) {
	slog.Debug("respuesta pendiente de SIESA: el paciente está en otra conversación, se espera",
		"phone", utils.MaskPhone(row.Phone), "appointment_id", row.AppointmentID, "action", row.Action)
}

// isOwnCancelSession dice si s es la pregunta «¿Confirmas que deseas cancelar?» de ESTA cita
// (CONFIRM_CANCEL_NOTIF con su notif_appt_id), abierta por una pasada anterior.
func (m *NotificationManager) isOwnCancelSession(ctx context.Context, s *session.Session, apptID string) (bool, error) {
	if s == nil || s.CurrentState != sm.StateConfirmCancelNotif {
		return false, nil
	}
	id, err := m.sessionRepo.GetContext(ctx, s.ID, "notif_appt_id")
	return id == apptID, err
}

// sessionInUse dice si la sesión que ocupa el teléfono es una conversación en curso: escalada, o
// activa y sin vencer. Una activa ya vencida no es uso: openConfirmCancelSession la cierra.
func sessionInUse(s *session.Session, now time.Time) bool {
	if s == nil {
		return false
	}
	return s.Status == session.StatusEscalated || s.ExpiresAt.After(now)
}

// siesaCheck decide con la cita recién leída: "" = se puede aplicar la acción de la fila;
// already_applied = ya está como pidió el paciente; not_applicable = cambió y ya no la admite: no
// existe, cambió de fecha u hora respecto de la recordada (la movieron con el mismo id) o, para
// confirmar, la cancelaron. Una fila sin fecha y hora guardadas no compara el horario.
func siesaCheck(row SiesaPendingRow, appt *domain.Appointment) string {
	if appt == nil || (row.AppointmentSlot != "" && appt.TimeSlot != row.AppointmentSlot) {
		return siesaOutcomeNotApplicable
	}
	switch row.Action {
	case siesaActionConfirm:
		if appt.Confirmed {
			return siesaOutcomeAlreadyApplied
		}
		if appt.Canceled {
			return siesaOutcomeNotApplicable
		}
	case siesaActionCancel:
		// Una cita ya confirmada se puede cancelar, como al pulsar «Cancelar» con SIESA arriba.
		if appt.Canceled {
			return siesaOutcomeAlreadyApplied
		}
	default:
		return siesaOutcomeNotApplicable
	}
	return ""
}

// startsWithin dice si la cita empieza antes de now+lead (o ya empezó). Sin hora legible, no.
func startsWithin(appt *domain.Appointment, now time.Time, lead time.Duration) bool {
	start, err := time.ParseInLocation("200601021504", appt.TimeSlot, time.Local)
	if err != nil {
		return false
	}
	return start.Before(now.Add(lead))
}

// recordSiesaFailure suma un intento: SIESA (o la BD local al abrir la sesión) sigue fallando y la
// fila espera a la pasada siguiente.
func (m *NotificationManager) recordSiesaFailure(ctx context.Context, row SiesaPendingRow, cause error) {
	if err := m.siesaPending.RecordSiesaAttempt(ctx, row.ID); err != nil {
		slog.Error("respuesta pendiente de SIESA: anotar intento", "id", row.ID, "error", err)
	}
	observability.Emit(observability.TraceNotif(row.AppointmentID), "notif_recordatorio", "siesa_retry_failed",
		observability.EmitOpts{Phone: row.Phone, Reason: row.Action, Attrs: map[string]interface{}{"n": row.Attempts + 1}})
	slog.Warn("respuesta pendiente de SIESA: no se pudo aplicar, se reintenta",
		"phone", utils.MaskPhone(row.Phone), "appointment_id", row.AppointmentID, "action", row.Action,
		"attempts", row.Attempts+1, "error", cause)
}

// finishSiesaRow saca la fila de pendientes con su desenlace. Devuelve false si no la sacó (la BD
// falló: la pasada siguiente la vuelve a ver; o ya estaba fuera: nadie más la toca).
func (m *NotificationManager) finishSiesaRow(ctx context.Context, row SiesaPendingRow, outcome string) bool {
	done, err := m.siesaPending.FinishSiesaPending(ctx, row.ID, outcome)
	if err != nil {
		slog.Error("respuesta pendiente de SIESA: cerrar fila", "id", row.ID, "outcome", outcome, "error", err)
		return false
	}
	if done && outcome == siesaOutcomeAlreadyApplied {
		slog.Info("respuesta pendiente de SIESA: la cita ya estaba como pidió el paciente",
			"phone", utils.MaskPhone(row.Phone), "appointment_id", row.AppointmentID, "action", row.Action)
	}
	return done
}

// handOverSiesaRow pasa la fila a un agente sin pisar la conversación en curso del paciente:
//   - si el paciente está conversando (con el bot o con un agente) y no venció ningún tope (capped),
//     la fila sigue pendiente y se vuelve a mirar en la pasada siguiente;
//   - vencido el tope con el paciente aún conversando en Néctar, NO se escala (leaveUnattended):
//     escalar allí toma la conversación para un agente y deja la sesión del paciente ESCALATED a
//     mitad de flujo (escalatedNotifSession reusa la sesión vigente), y Néctar no tiene nota interna.
//     En Bird escalar no toca ninguna sesión, así que se escala como siempre.
//
// Se cierra la fila ANTES de escalar: así la escalación sale una sola vez aunque la pasada se repita
// (si la fila no se pudo cerrar, no se escala y la pasada siguiente lo reintenta). El precio: si la
// escalación misma falla (Bird o Néctar caídos), solo queda el error en el log; el monitor
// escalation_failing lo cubre.
func (m *NotificationManager) handOverSiesaRow(ctx context.Context, row SiesaPendingRow, pending *PendingNotification, outcome string, capped bool, now time.Time) {
	_, busy, err := m.patientSession(ctx, row, now)
	switch {
	case err != nil:
		m.recordSiesaFailure(ctx, row, err)
		return
	case busy && !capped:
		waitForPatient(row)
		return
	case busy && session.IsNectarSession(row.Phone):
		m.leaveUnattended(ctx, row, outcome)
		return
	}
	if !m.finishSiesaRow(ctx, row, outcome) {
		return
	}
	reason := escalationSiesaConfirm
	switch {
	case outcome == siesaOutcomeNotApplicable:
		reason = escalationSiesaChanged
	case row.Action == siesaActionCancel:
		reason = escalationSiesaCancel
	}
	m.escalateToAgent(pending, reason) //nolint:contextcheck // escalateToAgent no toma ctx por diseño (su propio timeout, como desde el timer)
}

// leaveUnattended cierra la fila como unattended y lo deja a la vista sin tocar la conversación del
// paciente: evento notification_siesa_unattended (dashboard/auditor), traza de error y una línea de
// ERROR en el log, que el AlertHandler sube a Telegram. reason es el desenlace que habría escalado.
func (m *NotificationManager) leaveUnattended(ctx context.Context, row SiesaPendingRow, reason string) {
	if !m.finishSiesaRow(ctx, row, siesaOutcomeUnattended) {
		return
	}
	if m.tracker != nil {
		m.tracker.LogEvent(ctx, "", row.Phone, "notification_siesa_unattended", map[string]interface{}{
			"appointment_id": row.AppointmentID,
			"action":         row.Action,
			"reason":         reason,
		})
	}
	observability.Emit(observability.TraceNotif(row.AppointmentID), "notif_recordatorio", "siesa_unattended",
		observability.EmitOpts{Phone: row.Phone, Reason: reason})
	slog.Error("respuesta pendiente de SIESA sin escalar: el paciente sigue conversando en Néctar; revisar la cita a mano",
		"phone", utils.MaskPhone(row.Phone), "appointment_id", row.AppointmentID, "action", row.Action, "reason", reason)
}

// notification arma el pending con el que se responde o se escala la fila.
func (r SiesaPendingRow) notification() *PendingNotification {
	return &PendingNotification{
		Type: r.Type, Phone: r.Phone, ContactPhone: r.ContactPhone, AppointmentID: r.AppointmentID,
		AppointmentSlot: r.AppointmentSlot, BirdMessageID: r.BirdMessageID, ConversationID: r.ConversationID,
	}
}
