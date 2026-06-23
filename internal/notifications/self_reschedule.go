package notifications

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/neuro-bot/neuro-bot/internal/services"
	"github.com/neuro-bot/neuro-bot/internal/session"
	"github.com/neuro-bot/neuro-bot/internal/utils"
)

// startSelfReschedule creates a new session at SEARCH_SLOTS pre-populated with
// the old appointment's data. The state machine handles slot search → selection → booking.
// When skipCancel is true (cancellation flow), the old appointment is NOT cancelled
// after the new one is created (it was already cancelled by the admin).
func (m *NotificationManager) startSelfReschedule(phone string, pending *PendingNotification, skipCancel bool) {
	ctx := context.Background()

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

	// Modelo multi-slot: una cita ocupa N slots (programacion_medico_detalle.IdCita).
	// El número de espacios a re-reservar es el conteo real de slots de la cita, NO
	// len(block) — que con este modelo siempre vale 1 sin importar el tamaño real.
	espacios := len(block)
	if n, err := m.apptSvc.SlotCountForAppointment(ctx, pending.AppointmentID); err == nil && n > espacios {
		espacios = n
	}
	if espacios < 1 {
		espacios = 1
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
		if pending.ConversationID != "" {
			m.birdClient.UpdateFeedItem(pending.ConversationID, pending.BirdMessageID,
				false, m.cfg.BirdTeamFallback, "")
		}
		return
	}

	// 4. Derive flags from Observations
	isContrasted := "0"
	if strings.Contains(appt.Observations, "Contrastada") {
		isContrasted = "1"
	}
	isSedated := "0"
	if strings.Contains(appt.Observations, "Sedaci") {
		isSedated = "1"
	}

	skipCancelStr := "0"
	if skipCancel {
		skipCancelStr = "1"
	}

	// 5. Build procedures_json required by createAppointmentHandler
	cups := make([]services.CUPSEntry, 0, len(appt.Procedures))
	for _, p := range appt.Procedures {
		cups = append(cups, services.CUPSEntry{
			Code:     p.CupCode,
			Name:     p.CupName,
			Quantity: 1,
		})
	}
	if len(cups) == 0 {
		cups = []services.CUPSEntry{{Code: cupsCode, Name: cupsName, Quantity: 1}}
	}
	groups := []services.CUPSGroup{{
		ServiceType: "general",
		Cups:        cups,
		Espacios:    espacios,
	}}
	proceduresJSON, _ := json.Marshal(groups)

	// 6. Create session at SEARCH_SLOTS with pre-populated context
	sess := &session.Session{
		ID:           uuid.New().String(),
		PhoneNumber:  phone,
		CurrentState: "SEARCH_SLOTS",
		Status:       session.StatusActive,
		ExpiresAt:    time.Now().Add(120 * time.Minute),
	}

	sessionCtx := map[string]string{
		// Patient data
		"patient_id":     appt.PatientID,
		"patient_name":   appt.PatientName,
		"patient_entity": appt.Entity,
		"patient_age":    "0", // Skip age restrictions (already validated)

		// Procedure data
		"cups_code":       cupsCode,
		"cups_name":       cupsName,
		"is_contrasted":   isContrasted,
		"is_sedated":      isSedated,
		"espacios":        fmt.Sprintf("%d", espacios),
		"procedures_json": string(proceduresJSON),

		// Flow control
		"total_procedures":      "1",
		"current_procedure_idx": "0",
		"menu_option":           "agendar",

		// Reschedule-specific keys
		"reschedule_appt_id":         pending.AppointmentID,
		"reschedule_skip_cancel":     skipCancelStr,
		"reschedule_conversation_id": pending.ConversationID,
		"reschedule_bird_msg_id":     pending.BirdMessageID,

		// Prefer same doctor (must be cedula, not cod_medi, for slot_service match)
		"preferred_doctor_doc": appt.DoctorDocument,
	}

	if err := m.sessionRepo.Create(ctx, sess); err != nil {
		slog.Error("self_reschedule: create session", "error", err)
		m.birdClient.SendText(phone, pending.ConversationID,
			"Lo sentimos, ocurrió un problema. Por favor intenta más tarde.")
		return
	}

	if err := m.sessionRepo.SetContextBatch(ctx, sess.ID, sessionCtx); err != nil {
		slog.Error("self_reschedule: set context", "error", err)
		m.sessionRepo.UpdateStatus(ctx, sess.ID, session.StatusCompleted) // cleanup orphan
		m.birdClient.SendText(phone, pending.ConversationID,
			"Lo sentimos, ocurrió un problema. Por favor intenta más tarde.")
		return
	}

	// 6. Send "searching..." message and enqueue virtual message
	m.birdClient.SendText(phone, pending.ConversationID, "Vamos a buscar horarios disponibles para *"+cupsName+"*...")
	m.workerPool.EnqueueVirtual(phone)

	// 7. Log KPI event
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
		"espacios", espacios,
		"block_size", len(block))
}
