package notifications

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/services"
	"github.com/neuro-bot/neuro-bot/internal/session"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
	"github.com/neuro-bot/neuro-bot/internal/utils"
)

// handleConfirmation processes responses to the daily confirmation template.
// NOTE: Caller (HandleResponse) already removed pending from sync.Map and DB via LoadAndDelete.
func (m *NotificationManager) handleConfirmation(phone, action string, pending *PendingNotification) {
	// Timeout para no colgar la goroutine si SIESA (BD compartida con la UI) tiene un lock
	// contendido en citas/programacion_medico_detalle (N-20).
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	switch action {
	case "confirm":
		appt, _, err := m.apptSvc.FindBlockByAppointmentID(ctx, pending.AppointmentID)
		if err != nil || appt == nil {
			slog.Error("confirm: find appointment", "error", err, "appointment_id", pending.AppointmentID)
			return
		}

		// Get ALL patient's appointments for this date (may span multiple agendas/doctors)
		allAppts, err := m.apptSvc.GetPatientAppointmentsForDate(ctx, appt.PatientID, appt.Date)
		if err != nil || len(allAppts) == 0 {
			allAppts = []domain.Appointment{*appt} // Fallback to single appointment
		}

		if err := m.apptSvc.ConfirmBlock(ctx, allAppts, "whatsapp_bot", pending.ConversationID); err != nil {
			// N-15: si el UPDATE a SIESA falló NO debemos decirle al paciente "cita confirmada"
			// (quedaría con confirmación falsa y sin reintento). Avisar + escalar a agente.
			slog.Error("confirm: confirm appointments failed, escalating", "error", err, "appointment_id", pending.AppointmentID)
			_, _ = m.birdClient.SendText(phone, pending.ConversationID, "Tuvimos un inconveniente al confirmar tu cita. Un agente te ayudará en un momento.")
			m.escalateToAgent(pending, escalationSystemFailure)
			return
		}

		// Build procedure names from ALL appointments (deduplicated)
		seen := make(map[string]bool)
		var procNames []string
		for _, a := range allAppts {
			for _, p := range a.Procedures {
				if p.CupName != "" && !seen[p.CupName] {
					seen[p.CupName] = true
					procNames = append(procNames, p.CupName)
				}
			}
		}
		proceduresText := strings.Join(procNames, " y ")
		if proceduresText == "" {
			proceduresText = "Procedimiento"
		}

		// Build confirmation message with details
		msg := fmt.Sprintf("Tu cita ha sido confirmada!\n\n"+
			"*Fecha:* %s\n"+
			"*Hora:* %s\n"+
			"*Procedimiento:* %s",
			utils.FormatFriendlyDate(appt.Date),
			services.FormatTimeSlot(appt.TimeSlot),
			proceduresText,
		)

		// Look up address and preparations from ALL appointments' procedures
		if m.procRepo != nil {
			var prepText string
			address := ""
			seenCup := make(map[string]bool)

			for _, a := range allAppts {
				for _, proc := range a.Procedures {
					if proc.CupCode == "" || seenCup[proc.CupCode] {
						continue
					}
					seenCup[proc.CupCode] = true
					p, err := m.procRepo.FindByCode(ctx, proc.CupCode)
					if err != nil || p == nil {
						continue
					}
					if address == "" && p.Address != "" {
						address = p.Address
					}
					if p.Preparation != "" {
						prepText += fmt.Sprintf("\n- Para *%s*: %s", proc.CupName, p.Preparation)
						if p.VideoURL != "" {
							prepText += fmt.Sprintf("\n  📹 Video: %s", p.VideoURL)
						}
						if p.AudioURL != "" {
							prepText += fmt.Sprintf("\n  🎵 Audio: %s", p.AudioURL)
						}
					}
				}
			}

			if address != "" {
				if m.addrMapper != nil {
					msg += "\n" + m.addrMapper.FormatAddress(address)
				} else {
					msg += fmt.Sprintf("\n*Dirección:* %s", address)
				}
			}
			if prepText != "" {
				msg += "\n\n*Preparación:*" + prepText
			}
		}

		msg += "\n\nRecuerda presentarte 30 minutos antes para realizar el proceso de facturación. Te esperamos!"

		m.birdClient.SendText(phone, pending.ConversationID, msg)

		if pending.ConversationID != "" {
			m.birdClient.CloseFeedItems(pending.ConversationID)
		}

		// Close any active session so inactivity reminders don't fire
		if m.sessionRepo != nil {
			if err := m.sessionRepo.CompleteActiveByPhone(ctx, phone); err != nil {
				slog.Error("confirm: close active session", "phone", utils.MaskPhone(phone), "error", err)
			}
		}

		// Log event
		if m.tracker != nil {
			elapsed := time.Since(pending.CreatedAt).Minutes()
			m.tracker.LogEvent(ctx, "", phone, "notification_confirmed",
				map[string]interface{}{
					"appointment_id":    pending.AppointmentID,
					"total_confirmed":   len(allAppts),
					"response_time_min": int(elapsed),
					"conversation_id":   pending.ConversationID,
				})
		}

		slog.Info("proactive confirmation success",
			"phone", utils.MaskPhone(phone),
			"appointment_id", pending.AppointmentID,
			"total_confirmed", len(allAppts))

	case "reschedule":
		m.startConfirmRescheduleSession(phone, pending)

	case "cancel":
		m.startConfirmCancelSession(phone, pending)
	}
}

// handleConfirmationTimeout implements a 4-step escalation chain:
//
//	Step 0 → Follow-up #1 (friendly text, NO agent)
//	Step 1 → Follow-up #2 (direct text, NO agent)
//	Step 2 → Safety escalation (IVR didn't run — escalate to agent)
//	Step 3 → Post-IVR timeout (normal escalation to agent)
//
// NOTE: Caller (handleTimeout) already removed pending from sync.Map and DB via LoadAndDelete.
// When ConfirmFollowupEnabled=false, the timer fires after the 1 PM IVR call and no further
// action is taken — the pending was already handled by the IVR task or expires silently.
func (m *NotificationManager) handleConfirmationTimeout(pending *PendingNotification) {
	if !m.cfg.ConfirmFollowupEnabled {
		slog.Debug("confirmation followup disabled, timeout silently cleaned up",
			"phone", utils.MaskPhone(pending.Phone))
		return
	}
	switch pending.RetryCount {
	case 0:
		// Step 1: Follow-up #1 — friendly text, NO agent assignment
		if _, err := m.birdClient.SendText(pending.Phone, pending.ConversationID,
			"¡Hola! Aún no recibimos tu respuesta sobre tu cita de mañana. "+
				"Por favor confirma, cancela o reprograma para que podamos gestionar tu espacio."); err != nil {
			slog.Error("followup1_send_failed", "phone", utils.MaskPhone(pending.Phone), "error", err)
		}

		pending.RetryCount = 1
		duration := time.Duration(safeHours(m.cfg.ConfirmFollowup2Hours, 3)) * time.Hour
		pending.Timer = time.AfterFunc(duration, func() { m.handleTimeout(pending.Phone) })
		m.pending.Store(pending.Phone, pending)

		if m.persister != nil {
			expiresAt := time.Now().Add(duration)
			if err := m.persister.Upsert(context.Background(), pending.Phone, pending.Type,
				pending.AppointmentID, pending.WaitingListID, pending.BirdMessageID, pending.ConversationID,
				pending.RetryCount, expiresAt); err != nil {
				slog.Error("persist followup1", "phone", utils.MaskPhone(pending.Phone), "error", err)
			}
		}

		slog.Info("confirmation followup 1 sent", "phone", utils.MaskPhone(pending.Phone))

	case 1:
		// Step 2: Follow-up #2 — direct text, NO agent assignment
		if _, err := m.birdClient.SendText(pending.Phone, pending.ConversationID,
			"Recordatorio: Tu cita de mañana aún no ha sido confirmada. "+
				"Si no recibimos respuesta, te llamaremos para confirmar."); err != nil {
			slog.Error("followup2_send_failed", "phone", utils.MaskPhone(pending.Phone), "error", err)
		}

		pending.RetryCount = 2
		// Safety-net timer: 2h (wait for 15:00 IVR task) + PostIVR minutes + 30min buffer
		duration := 2*time.Hour + time.Duration(safeMinutes(m.cfg.ConfirmPostIVRMinutes, 30)+30)*time.Minute
		pending.Timer = time.AfterFunc(duration, func() { m.handleTimeout(pending.Phone) })
		m.pending.Store(pending.Phone, pending)

		if m.persister != nil {
			expiresAt := time.Now().Add(duration)
			if err := m.persister.Upsert(context.Background(), pending.Phone, pending.Type,
				pending.AppointmentID, pending.WaitingListID, pending.BirdMessageID, pending.ConversationID,
				pending.RetryCount, expiresAt); err != nil {
				slog.Error("persist followup2", "phone", utils.MaskPhone(pending.Phone), "error", err)
			}
		}

		slog.Info("confirmation followup 2 sent", "phone", utils.MaskPhone(pending.Phone))

	case 2, 3:
		// Step 3/4: Escalate to agent (step 2 = IVR didn't run, step 3 = post-IVR)
		m.escalateToAgent(pending, escalationNoResponse)
	}
}

// escalationReason distingue POR QUÉ se escala, para que la nota al agente y el mensaje al
// paciente sean correctos (bug_004): no es lo mismo "el paciente no respondió" (cadena de
// timeout) que "el paciente SÍ confirmó pero falló la persistencia en SIESA" (N-15).
type escalationReason int

const (
	escalationNoResponse    escalationReason = iota // timeout: ni WhatsApp ni IVR
	escalationSystemFailure                         // el paciente respondió, pero el UPDATE a SIESA falló
)

// escalateToAgent sends an internal note to Bird Inbox, optionally messages the patient,
// and assigns the conversation to the best available agent.
func (m *NotificationManager) escalateToAgent(pending *PendingNotification, reason escalationReason) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// 1. Look up appointment details for the note
	appt, _, _ := m.apptSvc.FindBlockByAppointmentID(ctx, pending.AppointmentID)

	patientName := ""
	apptInfo := fmt.Sprintf("Cita ID: %s", pending.AppointmentID)
	if appt != nil {
		patientName = appt.PatientName
		apptInfo = fmt.Sprintf("Paciente: %s\nFecha: %s | Hora: %s\nProcedimiento: %s\nCita ID: %s",
			patientName,
			utils.FormatFriendlyDate(appt.Date),
			services.FormatTimeSlot(appt.TimeSlot),
			services.GetFirstCupName(*appt),
			pending.AppointmentID)
	}

	var note string
	switch reason {
	case escalationSystemFailure:
		note = "⚠️ El paciente CONFIRMÓ su cita vía WhatsApp, pero la persistencia en SIESA FALLÓ.\n" +
			"Revisar y confirmar la cita manualmente en el sistema (NO re-pedir confirmación al paciente).\n" + apptInfo
	default: // escalationNoResponse
		note = "Paciente NO confirmo cita de manana.\nNo respondio a: mensajes WhatsApp + llamada IVR.\n" + apptInfo
	}

	// 2. Internal note — visible ONLY in Bird Inbox (patient doesn't see it on WhatsApp)
	if pending.ConversationID != "" {
		m.birdClient.SendInternalText(pending.ConversationID, note)
	}

	// 3. Message to patient — solo en el caso de no-respuesta. En el fallo de sistema el paciente
	// ya recibió "Tuvimos un inconveniente..." y NO se le debe pedir confirmar lo ya confirmado.
	if reason == escalationNoResponse {
		_, _ = m.birdClient.SendText(pending.Phone, pending.ConversationID,
			"Un asistente del centro se comunicará contigo para confirmar tu cita de mañana.")
	}

	// 4. Assign to best available agent
	if pending.ConversationID != "" {
		m.birdClient.EscalateToAgent(
			pending.ConversationID, pending.Phone,
			m.cfg.BirdTeamFallback, "Call Center",
			patientName, m.cfg.BirdTeamFallback)
	}

	// 5. Log event
	if m.tracker != nil {
		m.tracker.LogEvent(ctx, "", pending.Phone, "notification_escalated_agent",
			map[string]interface{}{
				"appointment_id": pending.AppointmentID,
				"retry_count":    pending.RetryCount,
			})
	}

	slog.Info("confirmation escalated to agent",
		"phone", utils.MaskPhone(pending.Phone),
		"appointment_id", pending.AppointmentID,
		"retry_count", pending.RetryCount)
}

// startConfirmRescheduleSession creates a new session at CONFIRM_RESCHEDULE_NOTIF
// so the state machine handles the "1/2" confirmation with proper retry logic.
func (m *NotificationManager) startConfirmRescheduleSession(phone string, pending *PendingNotification) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	appt, block, err := m.apptSvc.FindBlockByAppointmentID(ctx, pending.AppointmentID)
	if err != nil || appt == nil {
		slog.Error("startConfirmRescheduleSession: find appointment", "error", err, "appointment_id", pending.AppointmentID)
		m.birdClient.SendText(phone, pending.ConversationID,
			"No pudimos encontrar tu cita. Por favor contacta a la clínica.")
		return
	}

	if m.sessionRepo == nil || m.workerPool == nil {
		slog.Error("startConfirmRescheduleSession: missing session/worker dependencies")
		m.birdClient.SendText(phone, pending.ConversationID,
			"Servicio temporalmente no disponible. Por favor intenta más tarde.")
		return
	}

	cupsCode := ""
	cupsName := ""
	if len(appt.Procedures) > 0 {
		cupsCode = appt.Procedures[0].CupCode
		cupsName = appt.Procedures[0].CupName
	}

	isContrasted := "0"
	if strings.Contains(appt.Observations, "Contrastada") {
		isContrasted = "1"
	}
	isSedated := "0"
	if strings.Contains(appt.Observations, "Sedaci") {
		isSedated = "1"
	}

	// Build procedures_json required by createAppointmentHandler
	cups := make([]services.CUPSEntry, 0, len(appt.Procedures))
	for _, p := range appt.Procedures {
		cups = append(cups, services.CUPSEntry{
			Code:     p.CupCode,
			Name:     p.CupName,
			Quantity: 1,
		})
	}
	if len(cups) == 0 && cupsCode != "" {
		cups = []services.CUPSEntry{{Code: cupsCode, Name: cupsName, Quantity: 1}}
	}
	groups := []services.CUPSGroup{{
		ServiceType: "general",
		Cups:        cups,
		Espacios:    len(block),
	}}
	proceduresJSON, _ := json.Marshal(groups)

	// Close any pre-existing session to avoid orphan inactivity timers
	if err := m.sessionRepo.CompleteActiveByPhone(ctx, phone); err != nil {
		slog.Error("startConfirmRescheduleSession: close old session", "phone", utils.MaskPhone(phone), "error", err)
	}

	sess := &session.Session{
		ID:             uuid.New().String(),
		PhoneNumber:    phone,
		CurrentState:   sm.StateConfirmRescheduleNotif,
		ConversationID: pending.ConversationID,
		Status:         session.StatusActive,
		ExpiresAt:      time.Now().Add(30 * time.Minute),
	}

	sessCtx := map[string]string{
		// Patient data
		"patient_id":     appt.PatientID,
		"patient_name":   appt.PatientName,
		"patient_entity": appt.Entity,
		"patient_age":    "0",

		// Procedure data
		"cups_code":       cupsCode,
		"cups_name":       cupsName,
		"is_contrasted":   isContrasted,
		"is_sedated":      isSedated,
		"espacios":        fmt.Sprintf("%d", len(block)),
		"procedures_json": string(proceduresJSON),

		// Flow control
		"total_procedures":      "1",
		"current_procedure_idx": "0",
		"menu_option":           "agendar",

		// Reschedule keys (used by SEARCH_SLOTS if confirmed)
		"reschedule_appt_id":         pending.AppointmentID,
		"reschedule_skip_cancel":     "0",
		"reschedule_conversation_id": pending.ConversationID,
		"reschedule_bird_msg_id":     pending.BirdMessageID,
		"preferred_doctor_doc":       appt.DoctorID,

		// Display keys for confirmation prompt
		"notif_appt_date": utils.FormatFriendlyDate(appt.Date),
		"notif_appt_time": services.FormatTimeSlot(appt.TimeSlot),
		"notif_cups_name": services.GetFirstCupName(*appt),
	}

	if err := m.sessionRepo.Create(ctx, sess); err != nil {
		slog.Error("startConfirmRescheduleSession: create session", "error", err)
		m.birdClient.SendText(phone, pending.ConversationID, "Lo sentimos, ocurrió un problema. Por favor intenta más tarde.")
		return
	}
	if err := m.sessionRepo.SetContextBatch(ctx, sess.ID, sessCtx); err != nil {
		slog.Error("startConfirmRescheduleSession: set context", "error", err)
		m.sessionRepo.UpdateStatus(ctx, sess.ID, session.StatusCompleted) // cleanup orphan
		m.birdClient.SendText(phone, pending.ConversationID, "Lo sentimos, ocurrió un problema. Por favor intenta más tarde.")
		return
	}

	m.workerPool.EnqueueVirtual(phone)
	slog.Info("confirm_reschedule_session created", "phone", utils.MaskPhone(phone), "appointment_id", pending.AppointmentID)
}

// startConfirmCancelSession creates a new session at CONFIRM_CANCEL_NOTIF
// so the state machine handles the "1/2" confirmation with proper retry logic.
func (m *NotificationManager) startConfirmCancelSession(phone string, pending *PendingNotification) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	appt, _, err := m.apptSvc.FindBlockByAppointmentID(ctx, pending.AppointmentID)
	if err != nil || appt == nil {
		slog.Error("startConfirmCancelSession: find appointment", "error", err, "appointment_id", pending.AppointmentID)
		m.birdClient.SendText(phone, pending.ConversationID,
			"No pudimos encontrar tu cita. Por favor contacta a la clínica.")
		return
	}

	if m.sessionRepo == nil || m.workerPool == nil {
		slog.Error("startConfirmCancelSession: missing session/worker dependencies")
		m.birdClient.SendText(phone, pending.ConversationID,
			"Servicio temporalmente no disponible. Por favor intenta más tarde.")
		return
	}

	// Get ALL patient's appointments for this date (may span multiple agendas/doctors)
	allAppts, err := m.apptSvc.GetPatientAppointmentsForDate(ctx, appt.PatientID, appt.Date)
	if err != nil || len(allAppts) == 0 {
		allAppts = []domain.Appointment{*appt}
	}

	// Collect all appointment IDs, procedure names, and CUPS codes
	allIDs := make([]string, len(allAppts))
	seen := make(map[string]bool)
	seenCups := make(map[string]bool)
	var procNames []string
	var cupsCodes []string
	for i, a := range allAppts {
		allIDs[i] = a.ID
		for _, p := range a.Procedures {
			if p.CupName != "" && !seen[p.CupName] {
				seen[p.CupName] = true
				procNames = append(procNames, p.CupName)
			}
			if p.CupCode != "" && !seenCups[p.CupCode] {
				seenCups[p.CupCode] = true
				cupsCodes = append(cupsCodes, p.CupCode)
			}
		}
	}
	allIDsJSON, _ := json.Marshal(allIDs)
	cupsJSON, _ := json.Marshal(cupsCodes)
	cupsText := strings.Join(procNames, " y ")
	if cupsText == "" {
		cupsText = services.GetFirstCupName(*appt)
	}

	// Close any pre-existing session to avoid orphan inactivity timers
	if err := m.sessionRepo.CompleteActiveByPhone(ctx, phone); err != nil {
		slog.Error("startConfirmCancelSession: close old session", "phone", utils.MaskPhone(phone), "error", err)
	}

	sess := &session.Session{
		ID:             uuid.New().String(),
		PhoneNumber:    phone,
		CurrentState:   sm.StateConfirmCancelNotif,
		ConversationID: pending.ConversationID,
		Status:         session.StatusActive,
		ExpiresAt:      time.Now().Add(30 * time.Minute),
	}

	sessCtx := map[string]string{
		"patient_id":        appt.PatientID,
		"patient_name":      appt.PatientName,
		"notif_appt_id":     pending.AppointmentID,
		"notif_appt_ids":    string(allIDsJSON),
		"notif_cups_codes":  string(cupsJSON),
		"notif_appt_date":   utils.FormatFriendlyDate(appt.Date),
		"notif_appt_time":   services.FormatTimeSlot(appt.TimeSlot),
		"notif_cups_name":   cupsText,
		"notif_bird_msg_id": pending.BirdMessageID,
	}

	if err := m.sessionRepo.Create(ctx, sess); err != nil {
		slog.Error("startConfirmCancelSession: create session", "error", err)
		m.birdClient.SendText(phone, pending.ConversationID, "Lo sentimos, ocurrió un problema. Por favor intenta más tarde.")
		return
	}
	if err := m.sessionRepo.SetContextBatch(ctx, sess.ID, sessCtx); err != nil {
		slog.Error("startConfirmCancelSession: set context", "error", err)
		m.sessionRepo.UpdateStatus(ctx, sess.ID, session.StatusCompleted) // cleanup orphan
		m.birdClient.SendText(phone, pending.ConversationID, "Lo sentimos, ocurrió un problema. Por favor intenta más tarde.")
		return
	}

	m.workerPool.EnqueueVirtual(phone)
	slog.Info("confirm_cancel_session created", "phone", utils.MaskPhone(phone), "appointment_id", pending.AppointmentID)
}
