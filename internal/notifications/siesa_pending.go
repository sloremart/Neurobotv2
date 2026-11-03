package notifications

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/config"
	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/observability"
	"github.com/neuro-bot/neuro-bot/internal/utils"
)

// Respuesta al recordatorio pendiente de SIESA (D-04). Si el paciente pulsa «Confirmar» o
// «Cancelar» y SIESA no responde, la respuesta queda marcada en su fila de notification_history
// (migración 047) y un proceso de fondo la aplica cuando SIESA vuelve.

// Acciones que pueden quedar pendientes de SIESA (columna siesa_pending).
const (
	siesaActionConfirm = "confirm"
	siesaActionCancel  = "cancel"
)

// Desenlaces de una respuesta que deja de estar pendiente (columna siesa_outcome).
const (
	siesaOutcomeApplied        = "applied"         // el proceso de fondo la aplicó en SIESA
	siesaOutcomeAlreadyApplied = "already_applied" // la cita ya estaba como pidió el paciente
	siesaOutcomeNotApplicable  = "not_applicable"  // la cita cambió: no se tocó y se escaló
	siesaOutcomeGaveUp         = "gave_up"         // venció un tope: se escaló
)

// Textos al paciente cuando su respuesta queda pendiente de SIESA.
const (
	siesaConfirmDeferredText = "Recibimos tu confirmación. La registraremos en unos minutos."
	siesaCancelDeferredText  = "Recibimos tu solicitud de cancelación. La registraremos en unos minutos."
	// siesaCancelDoneText es el texto de «cita cancelada» de CONFIRM_CANCEL_NOTIF
	// (confirmCancelNotifHandler, statemachine/handlers): el mismo que recibe quien cancela con SIESA arriba.
	siesaCancelDoneText = "Tu cita ha sido cancelada.\n\nSi deseas reprogramar, puedes escribirnos cuando lo necesites."
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
	ID             int64 // id de la fila de notification_history
	Phone          string
	Type           string
	AppointmentID  string
	BirdMessageID  string
	ConversationID string
	ContactPhone   string
	Action         string        // "confirm" | "cancel"
	Attempts       int           // intentos fallidos del proceso de fondo
	PendingFor     time.Duration // tiempo desde que quedó pendiente (medido en la BD)
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
func (m *NotificationManager) deferToSiesa(ctx context.Context, phone, action string, pending *PendingNotification, cause error) bool {
	if m.siesaPending == nil {
		return false
	}
	// El ctx de la respuesta puede venir gastado por la propia SIESA colgada: la marca va con uno nuevo.
	markCtx, cancel := utils.MarkContext(ctx)
	defer cancel()
	row := SiesaPendingRow{
		Phone: phone, Type: pending.Type, AppointmentID: pending.AppointmentID, BirdMessageID: pending.BirdMessageID,
		ConversationID: pending.ConversationID, ContactPhone: pending.ContactPhone, Action: action,
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

// patientDayAppointments son las citas del paciente ese día (pueden ser de varias agendas); si no
// se pueden leer, solo appt.
func (m *NotificationManager) patientDayAppointments(ctx context.Context, appt *domain.Appointment) []domain.Appointment {
	all, err := m.apptSvc.GetPatientAppointmentsForDate(ctx, appt.PatientID, appt.Date)
	if err != nil || len(all) == 0 {
		return []domain.Appointment{*appt}
	}
	return all
}

// siesaPendingBatch acota las filas de una pasada: las que sobren van en la siguiente.
const siesaPendingBatch = 50

// siesaCancelReason es el motivo de cancelación del paciente por WhatsApp (el de CONFIRM_CANCEL_NOTIF).
const siesaCancelReason = "Cancelada por paciente via WhatsApp"

// StartSiesaPendingRetrier aplica en SIESA, cada cfg.SiesaPendingRetryMin minutos, las respuestas
// al recordatorio que quedaron pendientes. Las pasadas son secuenciales (un solo bucle): dos
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

// retrySiesaRow intenta aplicar UNA respuesta pendiente. Releer la cita antes de escribir hace de
// SIESA la llave de idempotencia: si una pasada aplicó y envió pero no pudo cerrar la fila, la
// siguiente encuentra la cita ya como pidió el paciente y la cierra sin reenviar nada.
func (m *NotificationManager) retrySiesaRow(ctx context.Context, row SiesaPendingRow, now time.Time) {
	if !m.lockPhone(row.Phone) { //nolint:contextcheck // el candado del manager acota su propia espera
		return // la pasada siguiente la retoma
	}
	defer m.unlockPhone(row.Phone)

	pending := row.notification()
	if row.PendingFor > siesaPendingMaxAge {
		m.handOverSiesaRow(ctx, row, pending, siesaOutcomeGaveUp)
		return
	}
	appt, _, err := m.apptSvc.FindBlockByAppointmentID(ctx, row.AppointmentID)
	if err != nil {
		m.recordSiesaFailure(ctx, row, err)
		return
	}
	switch siesaCheck(row.Action, appt) {
	case siesaOutcomeAlreadyApplied:
		m.finishSiesaRow(ctx, row, siesaOutcomeAlreadyApplied)
		return
	case siesaOutcomeNotApplicable:
		m.handOverSiesaRow(ctx, row, pending, siesaOutcomeNotApplicable)
		return
	}
	if startsWithin(appt, now, siesaPendingMinLead) {
		m.handOverSiesaRow(ctx, row, pending, siesaOutcomeGaveUp)
		return
	}

	allAppts := m.patientDayAppointments(ctx, appt)
	text, eventType := siesaCancelDoneText, "notification_cancel_confirmed"
	if row.Action == siesaActionConfirm {
		err = m.apptSvc.ConfirmBlock(ctx, allAppts, "whatsapp_bot", row.ConversationID)
		text, eventType = m.buildConfirmationMessage(ctx, appt, allAppts), "notification_confirmed"
	} else {
		err = m.apptSvc.CancelBlock(ctx, allAppts, siesaCancelReason, "whatsapp", row.ConversationID)
	}
	if err != nil {
		m.recordSiesaFailure(ctx, row, err)
		return
	}
	_, _ = m.birdClient.SendText(row.Phone, row.ConversationID, text) //nolint:contextcheck // cliente Bird sin ctx (patrón existente)

	markCtx, cancel := utils.MarkContext(ctx)
	defer cancel()
	m.finishSiesaRow(markCtx, row, siesaOutcomeApplied)
	if row.Action == siesaActionCancel {
		m.notifyFreedSlots(markCtx, allAppts)
	}
	if m.tracker != nil {
		m.tracker.LogEvent(markCtx, "", row.Phone, eventType, map[string]interface{}{
			"appointment_id":  row.AppointmentID,
			"total":           len(allAppts),
			"conversation_id": row.ConversationID,
			"siesa_deferred":  true,
		})
	}
	applied := observability.EmitOpts{Phone: row.Phone, Reason: "whatsapp_siesa_deferred"}
	if row.Action == siesaActionConfirm {
		observability.Emit(observability.TraceNotif(row.AppointmentID), "notif_recordatorio", "confirmed", applied)
	} else {
		observability.Emit(observability.TraceNotif(row.AppointmentID), "notif_recordatorio", "cancelled", applied)
	}
	slog.Info("respuesta pendiente de SIESA aplicada",
		"phone", utils.MaskPhone(row.Phone), "appointment_id", row.AppointmentID, "action", row.Action,
		"attempts", row.Attempts, "total", len(allAppts))
}

// siesaCheck decide con la cita recién leída: "" = se puede aplicar la acción; already_applied = ya
// está como pidió el paciente; not_applicable = cambió y ya no la admite (o no existe).
func siesaCheck(action string, appt *domain.Appointment) string {
	if appt == nil {
		return siesaOutcomeNotApplicable
	}
	switch action {
	case siesaActionConfirm:
		if appt.Confirmed {
			return siesaOutcomeAlreadyApplied
		}
		if appt.Canceled {
			return siesaOutcomeNotApplicable
		}
	case siesaActionCancel:
		if appt.Canceled {
			return siesaOutcomeAlreadyApplied
		}
		if appt.Confirmed { // la confirmaron después de que el paciente pidiera cancelar
			return siesaOutcomeNotApplicable
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

// recordSiesaFailure suma un intento: SIESA sigue sin responder y la fila espera a la pasada siguiente.
func (m *NotificationManager) recordSiesaFailure(ctx context.Context, row SiesaPendingRow, cause error) {
	if err := m.siesaPending.RecordSiesaAttempt(ctx, row.ID); err != nil {
		slog.Error("respuesta pendiente de SIESA: anotar intento", "id", row.ID, "error", err)
	}
	observability.Emit(observability.TraceNotif(row.AppointmentID), "notif_recordatorio", "siesa_retry_failed",
		observability.EmitOpts{Phone: row.Phone, Reason: row.Action, Attrs: map[string]interface{}{"n": row.Attempts + 1}})
	slog.Warn("respuesta pendiente de SIESA: SIESA sigue sin responder",
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

// handOverSiesaRow cierra la fila y escala a un agente. Se cierra ANTES de escalar: así la
// escalación sale una sola vez aunque la pasada se repita (si la fila no se pudo cerrar, no se
// escala y la pasada siguiente lo reintenta).
func (m *NotificationManager) handOverSiesaRow(ctx context.Context, row SiesaPendingRow, pending *PendingNotification, outcome string) {
	if !m.finishSiesaRow(ctx, row, outcome) {
		return
	}
	reason := escalationSiesaChanged
	if outcome == siesaOutcomeGaveUp {
		reason = escalationSystemFailure // «el paciente confirmó y SIESA falló»: la nota y el menú de siempre
		if row.Action == siesaActionCancel {
			reason = escalationSiesaCancelFailed
		}
	}
	m.escalateToAgent(pending, reason) //nolint:contextcheck // escalateToAgent no toma ctx por diseño (su propio timeout, como desde el timer)
}

// notifyFreedSlots avisa a la lista de espera por cada (médico, agenda) liberado al cancelar, como
// la cancelación de CONFIRM_CANCEL_NOTIF.
func (m *NotificationManager) notifyFreedSlots(ctx context.Context, block []domain.Appointment) {
	seen := make(map[[2]int]bool)
	for _, appt := range block {
		cod, _ := strconv.Atoi(appt.DoctorID)
		key := [2]int{cod, appt.AgendaID}
		if cod <= 0 || appt.AgendaID <= 0 || seen[key] {
			continue
		}
		seen[key] = true
		go m.CheckWaitingListForSlot(context.WithoutCancel(ctx), cod, appt.AgendaID)
	}
}

// notification arma el pending con el que se responde o se escala la fila.
func (r SiesaPendingRow) notification() *PendingNotification {
	return &PendingNotification{
		Type: r.Type, Phone: r.Phone, ContactPhone: r.ContactPhone, AppointmentID: r.AppointmentID,
		BirdMessageID: r.BirdMessageID, ConversationID: r.ConversationID,
	}
}
