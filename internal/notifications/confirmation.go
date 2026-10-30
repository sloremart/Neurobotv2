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
			_ = m.birdClient.CloseFeedItems(pending.Phone, pending.ConversationID)
		}

		// Cerrar sesión activa y abrir una nueva en StateGreeting con post_confirmed=1.
		// Si el paciente escribe algo (o Bird reintenta el postback), el bot muestra el menú
		// directamente sin volver a enviar el texto de política de datos (ya fue aceptado antes).
		// NO se llama EnqueueVirtual: no queremos enviar ningún mensaje extra de forma proactiva.
		if m.sessionRepo != nil {
			if err := m.sessionRepo.CompleteActiveByPhone(ctx, phone); err != nil {
				slog.Error("confirm: close active session", "phone", utils.MaskPhone(phone), "error", err)
			}
			convID := pending.ConversationID
			if convID == "" {
				if cached := m.birdClient.GetCachedConversationID(phone); cached != "" {
					convID = cached
				}
			}
			sess := &session.Session{
				ID:             uuid.New().String(),
				PhoneNumber:    phone,
				CurrentState:   sm.StateGreeting,
				ConversationID: convID,
				Status:         session.StatusActive,
				ExpiresAt:      time.Now().Add(30 * time.Minute),
			}
			postCtx := map[string]string{"post_confirmed": "1"}
			attachNewSessionContact(sess, postCtx, pending.sessionContact())
			if err := m.sessionRepo.Create(ctx, sess); err != nil {
				slog.Warn("confirm: create post-confirm session", "phone", utils.MaskPhone(phone), "error", err)
			} else if err := m.sessionRepo.SetContextBatch(ctx, sess.ID, postCtx); err != nil {
				slog.Warn("confirm: set post_confirmed context", "phone", utils.MaskPhone(phone), "error", err)
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
		m.closeIdleNectarConversation(pending)
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
		m.closeIdleNectarConversation(pending)
		return
	}
	// Los TEXTOS de followup por WhatsApp fueron eliminados (06-ago-2026): eran texto libre que
	// WhatsApp no entrega fuera de la ventana de servicio de 24h (el no-respondedor por definición
	// la tiene cerrada), pero Bird cobraba cada envío. Los pasos 0/1 solo avanzan RetryCount y
	// timers — la MISMA temporización de siempre — para que el IVR de las 15:00 (objetivo
	// RetryCount==2) y la escalación a agente por no-respuesta sigan intactos.
	switch pending.RetryCount {
	case 0:
		// Step 1: avanzar cadena (sin texto WA)
		slog.Info("followup step 1: sin texto WA (eliminado), cadena avanza",
			"phone", utils.MaskPhone(pending.Phone))

		pending.RetryCount = 1
		duration := time.Duration(safeHours(m.cfg.ConfirmFollowup2Hours, 3)) * time.Hour
		pending.ExpiresAt = time.Now().Add(duration) // N-19
		pending.Timer = time.AfterFunc(duration, func() { m.handleTimeout(pending.Phone) })
		m.storePending(pending.Phone, pending)

		if m.persister != nil {
			if err := m.persister.Upsert(context.Background(), pending.Phone, pending.Type,
				pending.AppointmentID, pending.WaitingListID, pending.BirdMessageID, pending.ConversationID, pending.ContactPhone,
				pending.Unsent, pending.RetryCount, pending.ExpiresAt); err != nil {
				slog.Error("persist followup1", "phone", utils.MaskPhone(pending.Phone), "error", err)
			}
		}

	case 1:
		// Step 2: avanzar cadena (sin texto WA); el safety-net espera al IVR de las 15:00
		slog.Info("followup step 2: sin texto WA (eliminado), cadena avanza hacia IVR",
			"phone", utils.MaskPhone(pending.Phone))

		pending.RetryCount = 2
		// Safety-net timer: 2h (wait for 15:00 IVR task) + PostIVR minutes + 30min buffer
		duration := 2*time.Hour + time.Duration(safeMinutes(m.cfg.ConfirmPostIVRMinutes, 30)+30)*time.Minute
		pending.ExpiresAt = time.Now().Add(duration) // N-19
		pending.Timer = time.AfterFunc(duration, func() { m.handleTimeout(pending.Phone) })
		m.storePending(pending.Phone, pending)

		if m.persister != nil {
			if err := m.persister.Upsert(context.Background(), pending.Phone, pending.Type,
				pending.AppointmentID, pending.WaitingListID, pending.BirdMessageID, pending.ConversationID, pending.ContactPhone,
				pending.Unsent, pending.RetryCount, pending.ExpiresAt); err != nil {
				slog.Error("persist followup2", "phone", utils.MaskPhone(pending.Phone), "error", err)
			}
		}

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

	// RESOLUCIÓN DE CANAL antes de rendirse (H130-1/H142-2: ~60/día de escalation_no_conversation —
	// casi la MITAD de estas escalaciones no llegaban al agente). Este pipeline es anterior al fix
	// del handoff del state machine y solo intentaba caché+lookup (que fallan justo para el
	// no-respondedor típico, que jamás ha escrito). Escalera portada de allá, ambas capas VALIDADAS
	// EN VIVO: (b) resolver el conversationId del PROPIO template enviado (pending.BirdMessageID se
	// persiste y sobrevive reinicios; a esta altura, ~9h después del envío, Bird ya lo asoció);
	// (d) crear la conversación explícitamente — el 409 devuelve la existente en details.
	if pending.ConversationID == "" {
		if cached := m.birdClient.GetCachedConversationID(pending.Phone); cached != "" {
			pending.ConversationID = cached
		}
	}
	if pending.ConversationID == "" && pending.BirdMessageID != "" {
		if resolved := m.birdClient.FetchMessageConversationID(ctx, pending.BirdMessageID); resolved != "" {
			pending.ConversationID = resolved
			m.birdClient.CacheConversationID(pending.Phone, resolved)
			slog.Info("notif escalation: conversation resuelta del propio template",
				"phone", utils.MaskPhone(pending.Phone), "conversation_id", resolved)
		}
	}
	if pending.ConversationID == "" {
		if created, cerr := m.birdClient.CreateConversationForPhone(ctx, pending.Phone); cerr == nil && created != "" {
			pending.ConversationID = created
			slog.Info("notif escalation: conversation creada como ultimo recurso",
				"phone", utils.MaskPhone(pending.Phone), "conversation_id", created)
		} else if cerr != nil {
			slog.Warn("notif escalation: crear conversation fallo",
				"phone", utils.MaskPhone(pending.Phone), "error", cerr)
		}
	}

	// 1. Look up appointment details for the note
	appt, _, _ := m.apptSvc.FindBlockByAppointmentID(ctx, pending.AppointmentID)

	patientName := ""
	apptInfo := fmt.Sprintf("Cita ID: %s", pending.AppointmentID)
	var apptDate, apptTime, cupsName, cupsCode string
	if appt != nil {
		patientName = appt.PatientName
		apptDate = utils.FormatFriendlyDate(appt.Date)
		apptTime = services.FormatTimeSlot(appt.TimeSlot)
		if len(appt.Procedures) > 0 {
			cupsName = appt.Procedures[0].CupName
			cupsCode = appt.Procedures[0].CupCode
		}
		apptInfo = fmt.Sprintf("Paciente: %s\nFecha: %s | Hora: %s\nProcedimiento: %s\nCita ID: %s",
			patientName,
			utils.FormatFriendlyDate(appt.Date),
			services.FormatTimeSlot(appt.TimeSlot),
			services.GetFirstCupName(*appt),
			pending.AppointmentID)
	}

	detail := sm.EscalationReasonNotifNoResponse
	if reason == escalationSystemFailure {
		detail = sm.EscalationReasonNotifSystemFailure
	}
	sessCtx := map[string]string{
		"notif_appointment_id":      pending.AppointmentID,
		"notif_type":                pending.Type,
		"notif_appt_date":           apptDate,
		"notif_appt_time":           apptTime,
		"notif_cups_name":           cupsName,
		sm.CtxNotifCupsCode:         cupsCode,
		"notif_conv_id":             pending.ConversationID,
		"patient_name":              patientName,
		"pre_escalation_state":      sm.StateNotifPending,
		sm.CtxNotifEscalationDetail: detail,
	}

	var note string
	switch reason {
	case escalationSystemFailure:
		note = "⚠️ El paciente CONFIRMÓ su cita vía WhatsApp, pero la persistencia en SIESA FALLÓ.\n" +
			"Revisar y confirmar la cita manualmente en el sistema (NO re-pedir confirmación al paciente).\n" + apptInfo
	default: // escalationNoResponse
		// El teléfono a marcar (DialPhone): con Néctar la clave es "nectar:<id>", que no se puede llamar.
		note = buildNoResponseNote(pending.SameDay, pending.DialPhone(), apptInfo)
	}

	// 2. La nota interna (visible SOLO en el Inbox; el paciente no la ve) viaja como resumen de la
	// escalación del paso 4. Sin ConversationID no hay nota NI asignación: el agente jamás se entera.
	// Emitir el hueco para que el auditor/dashboard lo cuenten en vez de perderse en silencio.
	if pending.ConversationID == "" {
		observability.Emit(observability.TraceNotif(pending.AppointmentID), "notif_recordatorio", "escalation_no_conversation",
			observability.EmitOpts{Phone: pending.Phone})
		slog.Warn("notif escalation without conversation_id - agent will NOT be notified",
			"phone", utils.MaskPhone(pending.Phone), "appointment_id", pending.AppointmentID)
	}

	// 3. Message to patient — solo en el caso de no-respuesta. En el fallo de sistema el paciente
	// ya recibió "Tuvimos un inconveniente..." y NO se le debe pedir confirmar lo ya confirmado.
	// Gate de ventana WA: el no-respondedor casi siempre la tiene cerrada — el texto se cobraría
	// sin entregarse. La escalación (nota interna + asignación de agente) ocurre igual.
	if reason == escalationNoResponse && m.WindowOpen(ctx, pending.Phone) {
		_, _ = m.birdClient.SendText(pending.Phone, pending.ConversationID,
			"Un asistente del centro se comunicará contigo para confirmar tu cita de "+followupDayWord(pending.SameDay)+".")
	}

	// 4. Assign to best available agent
	if pending.ConversationID != "" {
		// L9: capturar el error de la escalación (antes se descartaba). La escalación ya loguea
		// internamente en rutas degradadas, pero sin esto la traza propia no distinguía éxito/fallo.
		if _, _, err := m.birdClient.EscalateToAgentWithSummary(
			ctx, pending.ConversationID, pending.Phone,
			m.cfg.BirdTeamFallback, "Call Center",
			patientName, m.cfg.BirdTeamFallback,
			notifEscalation(m.cfg, pending.Phone, &session.Session{Context: sessCtx}, detail, note),
		); err != nil {
			slog.Error("notif escalate to agent failed", "phone", utils.MaskPhone(pending.Phone),
				"conversation_id", pending.ConversationID, "error", err)
		} else if session.IsNectarSession(pending.Phone) {
			// P0-2: en Néctar el retorno del agente necesita una sesión escalada en NOTIF_PENDING
			// (sin ella el worker lo descarta). Se persiste SOLO tras escalar con éxito: una sesión
			// ESCALATED sin agente dejaría al paciente sin bot hasta el barrido de vencidas. En Bird
			// no se crea, como siempre.
			m.registerNectarNotifEscalation(ctx, pending.Phone, pending.ConversationID, pending.sessionContact(), sessCtx)
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

// registerNectarNotifEscalation persiste la sesión escalada de una escalación de confirmación ya
// hecha en Néctar y la registra como escalateNotifToAgent (KPI de escaladas y SLA por escalación,
// from_state = NOTIF_PENDING).
func (m *NotificationManager) registerNectarNotifEscalation(ctx context.Context, phone, convID, contact string, sessCtx map[string]string) {
	sessID := m.escalatedNotifSession(ctx, phone, convID, contact, sessCtx)
	if sessID == "" {
		return
	}
	if m.tracker != nil {
		m.tracker.LogEvent(ctx, sessID, phone, "escalated_to_agent", map[string]interface{}{
			"from_state": sm.StateNotifPending,
			"agent":      "Call Center",
		})
	}
	if m.escalations != nil {
		if err := m.escalations.Create(ctx, sessID, phone, sm.StateNotifPending, m.cfg.BirdTeamFallback, "", "Call Center"); err != nil {
			slog.Warn("notif escalation: record escalation", "error", err, "phone", utils.MaskPhone(phone))
		}
	}
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

	// Datos de la cita y claves de reprogramación (usadas por SEARCH_SLOTS si confirma): el
	// armado compartido con startSelfReschedule y con «Reprogramar» de NOTIF_PENDING.
	sessCtx := m.apptSvc.RescheduleContext(ctx, appt, len(block))
	sessCtx["reschedule_conversation_id"] = pending.ConversationID
	sessCtx["reschedule_bird_msg_id"] = pending.BirdMessageID
	// Display keys for confirmation prompt
	sessCtx["notif_appt_date"] = utils.FormatFriendlyDate(appt.Date)
	sessCtx["notif_appt_time"] = services.FormatTimeSlot(appt.TimeSlot)
	sessCtx["notif_cups_name"] = services.GetFirstCupName(*appt)
	attachNewSessionContact(sess, sessCtx, pending.sessionContact())

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
	attachNewSessionContact(sess, sessCtx, pending.sessionContact())

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
