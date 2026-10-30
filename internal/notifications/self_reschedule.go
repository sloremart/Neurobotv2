package notifications

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/services"
	"github.com/neuro-bot/neuro-bot/internal/session"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
	"github.com/neuro-bot/neuro-bot/internal/utils"
)

// startSelfReschedule creates a new session at SEARCH_SLOTS pre-populated with
// the old appointment's data. The state machine handles slot search → selection → booking.
// When skipCancel is true (cancellation flow), the old appointment is NOT cancelled
// after the new one is created (it was already cancelled by the admin).
func (m *NotificationManager) startSelfReschedule(phone string, pending *PendingNotification, skipCancel bool) {
	// N-46: acotar el ctx para los writes a la BD local (Create/SetContextBatch/UpdateStatus)
	// y demás llamadas — sin deadline un cuelgue de la BD bloquea la goroutine indefinidamente.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// 1. Fetch old appointment + consecutive block
	appt, block, err := m.apptSvc.FindBlockByAppointmentID(ctx, pending.AppointmentID)
	if err != nil || appt == nil {
		slog.Error("self_reschedule: find appointment", "error", err, "appointment_id", pending.AppointmentID)
		m.birdClient.SendText(phone, pending.ConversationID,
			"No pudimos encontrar tu cita. Por favor contacta a la clínica.")
		return
	}

	// 2. Verify session dependencies are available
	if m.sessionRepo == nil || m.workerPool == nil {
		slog.Error("self_reschedule: missing session/worker dependencies")
		m.birdClient.SendText(phone, pending.ConversationID,
			"Servicio temporalmente no disponible. Por favor intenta más tarde.")
		return
	}

	// 3. Extract procedure info
	cupsCode := ""
	cupsName := ""
	if len(appt.Procedures) > 0 {
		cupsCode = appt.Procedures[0].CupCode
		cupsName = appt.Procedures[0].CupName
	}
	if cupsCode == "" {
		slog.Error("self_reschedule: no procedure on appointment", "appointment_id", pending.AppointmentID)
		m.birdClient.SendText(phone, pending.ConversationID,
			"No pudimos identificar el procedimiento de tu cita. Un agente te ayudará.")
		if session.IsNectarSession(phone) {
			m.escalateNoProcedure(ctx, phone, pending, appt)
		} else if pending.ConversationID != "" {
			m.birdClient.UpdateFeedItem(pending.ConversationID, pending.BirdMessageID,
				false, m.cfg.BirdTeamFallback, "")
		}
		return
	}

	// 4. Create session at SEARCH_SLOTS with pre-populated context
	sess := &session.Session{
		ID:           uuid.New().String(),
		PhoneNumber:  phone,
		CurrentState: "SEARCH_SLOTS",
		Status:       session.StatusActive,
		ExpiresAt:    time.Now().Add(120 * time.Minute),
	}

	// Datos de la cita y claves de reprogramación: el armado compartido con
	// startConfirmRescheduleSession y con «Reprogramar» de NOTIF_PENDING.
	sessionCtx := m.apptSvc.RescheduleContext(ctx, appt, len(block))
	if skipCancel {
		sessionCtx["reschedule_skip_cancel"] = "1"
	}
	sessionCtx["reschedule_conversation_id"] = pending.ConversationID
	sessionCtx["reschedule_bird_msg_id"] = pending.BirdMessageID
	attachNewSessionContact(sess, sessionCtx, pending.sessionContact())

	if err := m.sessionRepo.Create(ctx, sess); err != nil {
		slog.Error("self_reschedule: create session", "error", err)
		m.birdClient.SendText(phone, pending.ConversationID,
			"Lo sentimos, ocurrió un problema. Por favor intenta más tarde.")
		return
	}
	if m.tracker != nil { // sesión proactiva: contar también en total_sessions
		m.tracker.LogEvent(ctx, sess.ID, phone, "session_started", map[string]interface{}{"proactive": true})
	}

	if err := m.sessionRepo.SetContextBatch(ctx, sess.ID, sessionCtx); err != nil {
		slog.Error("self_reschedule: set context", "error", err)
		m.sessionRepo.UpdateStatus(ctx, sess.ID, session.StatusCompleted) // cleanup orphan
		m.birdClient.SendText(phone, pending.ConversationID,
			"Lo sentimos, ocurrió un problema. Por favor intenta más tarde.")
		return
	}

	// 5. Send "searching..." message and enqueue virtual message
	m.birdClient.SendText(phone, pending.ConversationID, "Vamos a buscar horarios disponibles para *"+cupsName+"*...")
	m.workerPool.EnqueueVirtual(phone)

	// 6. Log KPI event
	if m.tracker != nil {
		m.tracker.LogEvent(ctx, sess.ID, phone, "notification_reschedule_self_service",
			map[string]interface{}{
				"appointment_id":    pending.AppointmentID,
				"cups_code":         cupsCode,
				"skip_cancel":       skipCancel,
				"notification_type": pending.Type,
				"conversation_id":   pending.ConversationID,
			})
	}

	slog.Info("self_reschedule session created",
		"phone", utils.MaskPhone(phone),
		"appointment_id", pending.AppointmentID,
		"cups_code", cupsCode,
		"skip_cancel", skipCancel,
		"espacios", sessionCtx["espacios"],
		"block_size", len(block))
}

// escalateNoProcedure entrega a un agente de Néctar el «Reprogramar» que el bot no puede atender
// porque la cita no tiene procedimiento en SIESA. En Bird el traspaso es UpdateFeedItem; en Néctar
// ese PATCH iría a Bird con un id de Néctar y el paciente esperaría a un agente que nunca llega.
// La sesión escalada (retorno del agente) se persiste solo si la escalación salió.
func (m *NotificationManager) escalateNoProcedure(ctx context.Context, phone string, pending *PendingNotification, appt *domain.Appointment) {
	apptDate := utils.FormatFriendlyDate(appt.Date)
	apptTime := services.FormatTimeSlot(appt.TimeSlot)
	sessCtx := map[string]string{
		"notif_appointment_id":      pending.AppointmentID,
		"notif_type":                pending.Type,
		"notif_appt_date":           apptDate,
		"notif_appt_time":           apptTime,
		"notif_conv_id":             pending.ConversationID,
		"patient_name":              appt.PatientName,
		"pre_escalation_state":      sm.StateNotifPending,
		sm.CtxNotifEscalationDetail: sm.EscalationReasonRescheduleNoProcedure,
	}
	note := fmt.Sprintf("El paciente pidió reprogramar su cita, pero la cita no tiene procedimiento (CUPS) en SIESA.\n"+
		"Reprogramarla a mano.\nPaciente: %s\nFecha: %s | Hora: %s\nCita ID: %s",
		appt.PatientName, apptDate, apptTime, pending.AppointmentID)

	esc := notifEscalation(m.cfg, phone, &session.Session{Context: sessCtx}, sm.EscalationReasonRescheduleNoProcedure, note)
	if _, _, err := m.birdClient.EscalateToAgentWithSummary(ctx, pending.ConversationID, phone,
		m.cfg.BirdTeamFallback, "Call Center", appt.PatientName, m.cfg.BirdTeamFallback, esc); err != nil {
		slog.Error("self_reschedule: escalate no-procedure", "phone", utils.MaskPhone(phone),
			"appointment_id", pending.AppointmentID, "error", err)
		return
	}
	m.registerNectarNotifEscalation(ctx, phone, pending.ConversationID, pending.sessionContact(), sessCtx)
}
