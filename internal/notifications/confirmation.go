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
	"github.com/neuro-bot/neuro-bot/internal/observability"
	"github.com/neuro-bot/neuro-bot/internal/services"
	"github.com/neuro-bot/neuro-bot/internal/session"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
	"github.com/neuro-bot/neuro-bot/internal/utils"
)

// followupDayWord devuelve la palabra del día de la cita en los followups: "mañana" para el flujo
// preexistente del recordatorio del día antes (SameDay=false, default) y "hoy" para el recordatorio de
// corta antelación. Con false los textos quedan EXACTAMENTE como siempre (garantizado por test).
func followupDayWord(sameDay bool) string {
	if sameDay {
		return "hoy"
	}
	return "mañana"
}

// buildNoResponseNote arma la nota interna (solo agentes, Bird Inbox) de la escalación por
// no-respuesta, con ACCIÓN explícita y el teléfono para llamar de una (los agentes salen a las 18:00;
// la nota debe ser accionable de un vistazo). ASCII deliberado (sin tildes) como el resto de la nota.
func buildNoResponseNote(sameDay bool, phone, apptInfo string) string {
	dayNote := "manana"
	if sameDay {
		dayNote = "hoy"
	}
	return "Paciente NO confirmo cita de " + dayNote + ".\n" +
		"No respondio a: mensajes WhatsApp + llamada IVR.\n" +
		">> ACCION: LLAMAR al paciente para confirmar o liberar el cupo.\n" +
		">> Tel: " + phone + "\n" +
		apptInfo
}

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
			observability.Emit(observability.TraceNotif(pending.AppointmentID), "notif_recordatorio", "error",
				observability.EmitOpts{Phone: phone, Reason: "wa_confirm_persist"})
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
		msg := fmt.Sprintf(
			"Tu cita ha sido confirmada!\n\n"+
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
			mapsURL := ""
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
						mapsURL = p.MapsURL
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
					msg += "\n" + m.addrMapper.FormatAddress(address, mapsURL)
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

		observability.Emit(observability.TraceNotif(pending.AppointmentID), "notif_recordatorio", "confirmed",
			observability.EmitOpts{Phone: phone, Reason: "whatsapp"})
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
	// Recordatorio de corta antelación (D1, §8.1 #2): es de UN solo disparo. Si el paciente no
	// respondió al vencer el timer, se limpia EN SILENCIO — sin followups ni escalación (la cita ya
	// ocurrió o está por ocurrir; los followups del flujo normal asumen que la cita es mañana).
	// Solo se emite el evento para medirlo. El flujo preexistente (SameDay=false) nunca entra aquí.
	if pending.SameDay {
		observability.Emit(observability.TraceNotif(pending.AppointmentID), "notif_recordatorio", "same_day_no_response",
			observability.EmitOpts{Phone: pending.Phone})
		slog.Info("same-day reminder expired without response, silently cleaned up",
			"phone", utils.MaskPhone(pending.Phone), "appointment_id", pending.AppointmentID)
		return
	}
	// Palabra del día de la cita: "mañana" en todo el flujo preexistente (SameDay=false ⇒ textos
	// idénticos a los de siempre); "hoy" solo para el recordatorio de corta antelación.
	day := followupDayWord(pending.SameDay)

	switch pending.RetryCount {
	case 0:
		// Step 1: Follow-up #1 — friendly text, NO agent assignment
		if _, err := m.birdClient.SendText(pending.Phone, pending.ConversationID,
			"¡Hola! Aún no recibimos tu respuesta sobre tu cita de "+day+". "+
				"Por favor confirma, cancela o reprograma para que podamos gestionar tu espacio."); err != nil {
			slog.Error("followup1_send_failed", "phone", utils.MaskPhone(pending.Phone), "error", err)
		}

		pending.RetryCount = 1
		duration := time.Duration(safeHours(m.cfg.ConfirmFollowup2Hours, 3)) * time.Hour
		pending.ExpiresAt = time.Now().Add(duration) // N-19
		pending.Timer = time.AfterFunc(duration, func() { m.handleTimeout(pending.Phone) })
		m.pending.Store(pending.Phone, pending)

		if m.persister != nil {
			if err := m.persister.Upsert(context.Background(), pending.Phone, pending.Type,
				pending.AppointmentID, pending.WaitingListID, pending.BirdMessageID, pending.ConversationID,
				pending.RetryCount, pending.ExpiresAt); err != nil {
				slog.Error("persist followup1", "phone", utils.MaskPhone(pending.Phone), "error", err)
			}
		}

		slog.Info("confirmation followup 1 sent", "phone", utils.MaskPhone(pending.Phone))

	case 1:
		// Step 2: Follow-up #2 — direct text, NO agent assignment
		if _, err := m.birdClient.SendText(pending.Phone, pending.ConversationID,
			"Recordatorio: Tu cita de "+day+" aún no ha sido confirmada. "+
				"Si no recibimos respuesta, te llamaremos para confirmar."); err != nil {
			slog.Error("followup2_send_failed", "phone", utils.MaskPhone(pending.Phone), "error", err)
		}

		pending.RetryCount = 2
		// Safety-net timer: 2h (wait for 15:00 IVR task) + PostIVR minutes + 30min buffer
		duration := 2*time.Hour + time.Duration(safeMinutes(m.cfg.ConfirmPostIVRMinutes, 30)+30)*time.Minute
		pending.ExpiresAt = time.Now().Add(duration) // N-19
		pending.Timer = time.AfterFunc(duration, func() { m.handleTimeout(pending.Phone) })
		m.pending.Store(pending.Phone, pending)

		if m.persister != nil {
			if err := m.persister.Upsert(context.Background(), pending.Phone, pending.Type,
				pending.AppointmentID, pending.WaitingListID, pending.BirdMessageID, pending.ConversationID,
				pending.RetryCount, pending.ExpiresAt); err != nil {
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
		note = buildNoResponseNote(pending.SameDay, pending.Phone, apptInfo)
	}

	// 2. Internal note — visible ONLY in Bird Inbox (patient doesn't see it on WhatsApp)
	if pending.ConversationID != "" {
		m.birdClient.SendInternalText(pending.ConversationID, note)
	} else {
		// Sin ConversationID no hay nota NI asignación (paso 4): el agente jamás se entera. Emitir el
		// hueco para que el auditor/dashboard lo cuenten en vez de perderse en silencio.
		observability.Emit(observability.TraceNotif(pending.AppointmentID), "notif_recordatorio", "escalation_no_conversation",
			observability.EmitOpts{Phone: pending.Phone})
		slog.Warn("notif escalation without conversation_id - agent will NOT be notified",
			"phone", utils.MaskPhone(pending.Phone), "appointment_id", pending.AppointmentID)
	}

	// 3. Message to patient — solo en el caso de no-respuesta. En el fallo de sistema el paciente
	// ya recibió "Tuvimos un inconveniente..." y NO se le debe pedir confirmar lo ya confirmado.
	if reason == escalationNoResponse {
		_, _ = m.birdClient.SendText(pending.Phone, pending.ConversationID,
			"Un asistente del centro se comunicará contigo para confirmar tu cita de "+followupDayWord(pending.SameDay)+".")
	}

	// 4. Assign to best available agent
	if pending.ConversationID != "" {
		// L9: capturar el error de la escalación (antes se descartaba). EscalateToAgent ya loguea
		// internamente en rutas degradadas, pero sin esto la traza propia no distinguía éxito/fallo.
		if _, _, err := m.birdClient.EscalateToAgent(
			ctx, pending.ConversationID, pending.Phone,
			m.cfg.BirdTeamFallback, "Call Center",
			patientName, m.cfg.BirdTeamFallback,
		); err != nil {
			slog.Error("notif escalate to agent failed", "phone", utils.MaskPhone(pending.Phone),
				"conversation_id", pending.ConversationID, "error", err)
		}
	}

	// 5. Log event
	if m.tracker != nil {
		m.tracker.LogEvent(ctx, "", pending.Phone, "notification_escalated_agent",
			map[string]interface{}{
				"appointment_id": pending.AppointmentID,
				"retry_count":    pending.RetryCount,
			})
	}
	escReason := "no_response"
	if reason == escalationSystemFailure {
		escReason = "system_failure"
	}
	observability.Emit(observability.TraceNotif(pending.AppointmentID), "notif_recordatorio", "escalated",
		observability.EmitOpts{Phone: pending.Phone, Reason: escReason})

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

	// Modelo multi-slot: una cita ocupa N slots (programacion_medico_detalle.IdCita).
	// El número de espacios a re-reservar al reagendar es el conteo real de slots de la
	// cita, NO len(block) — que con este modelo siempre vale 1 sin importar el tamaño.
	espacios := len(block)
	if n, err := m.apptSvc.SlotCountForAppointment(ctx, pending.AppointmentID); err == nil && n > espacios {
		espacios = n
	}
	if espacios < 1 {
		espacios = 1
	}

	cupsCode := ""
	cupsName := ""
	if len(appt.Procedures) > 0 {
		cupsCode = appt.Procedures[0].CupCode
		cupsName = appt.Procedures[0].CupName
	}

	// Contraste: observación de SIESA + nombre del/los CUPS (cita de agente sin "Contrastada" en la
	// observación pero con "CON CONTRASTE" en el nombre → evita reprogramar como simple fuera de ventana).
	procNames := []string{cupsName}
	for _, p := range appt.Procedures {
		procNames = append(procNames, p.CupName)
	}
	isContrasted := "0"
	if utils.AppointmentIsContrasted(appt.Observations, procNames...) {
		isContrasted = "1"
	}
	// Sedación: observación + asunto de la cita (17 = agenda de sedación, señal fiable cuando el
	// agente no escribió la observación).
	asunto, _ := m.apptSvc.AppointmentAsunto(ctx, pending.AppointmentID)
	isSedated := "0"
	if utils.AppointmentIsSedated(appt.Observations, asunto) {
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
		Espacios:    espacios,
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
		// Contrato de la cita original → effectiveContract re-valida MRC/Evento por CUPS al
		// reprogramar (misma regla que al crear); sin esto quedaba vacío y solo se preservaba el
		// contrato original sin re-validar. Ver nota en self_reschedule.go.
		"patient_contract": appt.Entity,
		"patient_age":      "0",

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

		// Reschedule keys (used by SEARCH_SLOTS if confirmed)
		"reschedule_appt_id":         pending.AppointmentID,
		"reschedule_skip_cancel":     "0",
		"reschedule_conversation_id": pending.ConversationID,
		"reschedule_bird_msg_id":     pending.BirdMessageID,
		// N14: preferred_doctor_doc debe ser la cédula (sis_medi.cedula), no cod_medi —
		// slot_service hace match contra DoctorDocument. DoctorID nunca casaba.
		"preferred_doctor_doc": appt.DoctorDocument,

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
	if m.tracker != nil { // sesión proactiva: contar también en total_sessions
		m.tracker.LogEvent(ctx, sess.ID, phone, "session_started", map[string]interface{}{"proactive": true})
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
	if m.tracker != nil { // sesión proactiva: contar también en total_sessions
		m.tracker.LogEvent(ctx, sess.ID, phone, "session_started", map[string]interface{}{"proactive": true})
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
