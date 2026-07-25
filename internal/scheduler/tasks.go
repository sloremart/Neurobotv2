package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/config"
	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/notifications"
	"github.com/neuro-bot/neuro-bot/internal/observability"
	"github.com/neuro-bot/neuro-bot/internal/repository"
	"github.com/neuro-bot/neuro-bot/internal/services"
	"github.com/neuro-bot/neuro-bot/internal/utils"
)

// WaitingListRepo interface for waiting list operations in scheduler.
type WaitingListRepo interface {
	ExpireOld(ctx context.Context, days int) (int64, error)
	ExpireStaleNotified(ctx context.Context, hours int) (int64, error)
	GetDistinctWaitingCups(ctx context.Context) ([]string, error)
	GetWaitingByCups(ctx context.Context, cupsCode string, limit int) ([]domain.WaitingListEntry, error)
	UpdateStatus(ctx context.Context, id, status string) error
	MarkNotified(ctx context.Context, id string) (bool, error)
}

// EventLogger logs events for auditing (matches tracking.EventTracker).
type EventLogger interface {
	LogEvent(ctx context.Context, sessionID, phone, eventType string, data map[string]interface{})
}

// InboxCleaner cleans up processed inbox messages.
type InboxCleaner interface {
	DeleteOlderThan(ctx context.Context, hours int) (int64, error)
}

// NotificationHistory consulta si una cita ya recibió un recordatorio (evento notification_sent en
// chat_events). Lo usa el recordatorio de corta antelación para NO duplicar el de las 07:00: quien ya
// fue notificado ayer no recibe un segundo mensaje.
type NotificationHistory interface {
	WasAppointmentNotified(ctx context.Context, appointmentID string) (bool, error)
}

// ReengageSource entrega los telefonos candidatos al re-enganche matutino (par.8.1 #9).
type ReengageSource interface {
	PhonesForMorningReengage(ctx context.Context) ([]string, error)
}

// FlowMaintainer hace el rollup diario y la purga de la traza de flujos (Fase 3 observabilidad).
type FlowMaintainer interface {
	RollupDay(ctx context.Context, day time.Time) (int64, error)
	PurgeOlderThan(ctx context.Context, days int) (int64, error)
}

// Tasks holds dependencies for all scheduler tasks.
type Tasks struct {
	AppointmentRepo repository.AppointmentRepository
	AppointmentSvc  *services.AppointmentService // SOAT month filter for WL check
	BirdClient      *bird.Client
	NotifyManager   *notifications.NotificationManager
	WaitingListRepo WaitingListRepo
	SlotService     *services.SlotService
	ProcedureRepo   repository.ProcedureRepository // for IVR address lookup
	Cfg             *config.Config
	Tracker         EventLogger
	InboxRepo       InboxCleaner              // WAL cleanup (optional)
	Reconciler      *observability.Reconciler // reconciliación de invariantes (Fase 2, optional)
	FlowMaint       FlowMaintainer            // rollup + purga de flow_events (Fase 3, optional)
	ReengageSource  ReengageSource            // re-enganche matutino (optional; nil = tarea muda)
	NotifHistory    NotificationHistory       // dedup del recordatorio de corta antelación (optional; sin él la tarea NO envía — fail-closed)
}

// RegisterAll registers the scheduled tasks: the 4 originales (cleanup, recordatorios 07:00, lista de
// espera 06:00, IVR 13:00) + las corridas horarias del recordatorio de corta antelación (06–16h).
func (t *Tasks) RegisterAll(s *Scheduler) {
	// 02:00 — Data cleanup
	s.AddTask(ScheduledTask{
		Name: "data_cleanup",
		Hour: 2, Minute: 0,
		Fn: t.cleanup,
	})

	// 07:00 — WhatsApp reminders for tomorrow's appointments
	// Sunday included: Monday appointments need reminders sent on Sunday
	s.AddTask(ScheduledTask{
		Name: "whatsapp_reminders",
		Hour: 7, Minute: 0,
		Weekdays: []time.Weekday{
			time.Sunday, time.Monday, time.Tuesday, time.Wednesday,
			time.Thursday, time.Friday, time.Saturday,
		},
		Fn:      t.sendWhatsAppReminders,
		Catchup: CatchupDaytime, // L14: WA solo se recupera en horario diurno
	})

	// 07:05 - re-enganche matutino (par.8.1 #9): quienes rebotaron fuera de horario ayer >=17h
	// (picos 18-19h) reciben un "ya estamos disponibles" dentro de su ventana de 24h de WhatsApp.
	s.AddTask(ScheduledTask{
		Name: "morning_reengage",
		Hour: 7, Minute: 5,
		Weekdays: []time.Weekday{
			time.Monday, time.Tuesday, time.Wednesday,
			time.Thursday, time.Friday, time.Saturday,
		},
		Fn:      t.sendMorningReengagement,
		Catchup: CatchupNever,
	})

	// 06:00 — Waiting list check
	s.AddTask(ScheduledTask{
		Name:   "waiting_list_check_06",
		Hour:   6,
		Minute: 0,
		Weekdays: []time.Weekday{
			time.Monday, time.Tuesday, time.Wednesday,
			time.Thursday, time.Friday,
		},
		Fn:      t.checkWaitingList,
		Catchup: CatchupDaytime, // L14: WA solo se recupera en horario diurno
	})

	// 13:00 — IVR calls for non-responders
	// Sunday included: follows up WA reminders sent earlier that day
	s.AddTask(ScheduledTask{
		Name: "voice_reminders",
		Hour: 13, Minute: 0,
		Weekdays: []time.Weekday{
			time.Sunday, time.Monday, time.Tuesday, time.Wednesday,
			time.Thursday, time.Friday, time.Saturday,
		},
		Fn:      t.sendVoiceReminders,
		Catchup: CatchupNever, // L14: el IVR NO se recupera — solo corre a su hora tras el WA matutino
	})

	// 06:00–16:00 cada hora — recordatorio de CORTA ANTELACIÓN (§8.1 #2): citas de HOY agendadas
	// después de la corrida de las 07:00 de ayer, que por diseño nunca reciben el recordatorio del
	// día antes (25,6% de no-show medido en ese bucket). ADITIVO: no toca whatsapp_reminders.
	// Cada corrida capta las citas cuya hora cae en [ahora+1h, ahora+2h) — una sola vez por cita.
	// CatchupNever: una corrida perdida no se recupera (su ventana ya pasó); la siguiente cubre la suya.
	// Lun–Sáb (la clínica no opera domingo).
	for h := 6; h <= 16; h++ {
		s.AddTask(ScheduledTask{
			Name: fmt.Sprintf("same_day_reminders_%02d", h),
			Hour: h, Minute: 0,
			Weekdays: []time.Weekday{
				time.Monday, time.Tuesday, time.Wednesday,
				time.Thursday, time.Friday, time.Saturday,
			},
			Fn:      t.sendSameDayReminders,
			Catchup: CatchupNever,
		})
	}
}

// === Task 07:00: WhatsApp Reminders ===

// SendWhatsAppReminders is the public entry point for manual/test triggers.
func (t *Tasks) SendWhatsAppReminders(ctx context.Context) error {
	return t.sendWhatsAppReminders(ctx)
}

func (t *Tasks) sendWhatsAppReminders(ctx context.Context) error {
	if t.Cfg != nil && !t.Cfg.WhatsAppNotificationsEnabled {
		slog.Info("whatsapp reminders skipped — notifications disabled (WHATSAPP_NOTIFICATIONS_ENABLED=false)")
		return nil
	}

	tomorrow := time.Now().AddDate(0, 0, 1).Format("2006-01-02")

	appointments, err := t.AppointmentRepo.FindPendingByDate(ctx, tomorrow)
	if err != nil {
		return fmt.Errorf("fetch tomorrow appointments: %w", err)
	}

	// Group by patient
	patientGroups := groupAppointmentsByPatient(appointments)

	slog.Info("whatsapp reminders", "date", tomorrow, "slots", len(appointments), "patients", len(patientGroups))
	if len(appointments) == 0 {
		return nil
	}

	sent := 0
	skipped := 0

	for _, group := range patientGroups {
		// Detener envíos si el contexto fue cancelado (apagado)
		if ctx.Err() != nil {
			break
		}
		firstAppt := group[0]

		phone := utils.ParseColombianPhone(firstAppt.PatientPhone)
		if phone == "" {
			skipped++
			slog.Warn("skipping reminder - invalid phone",
				"patient_id", firstAppt.PatientID,
				"phone", utils.MaskPhone(firstAppt.PatientPhone))
			continue
		}

		if !t.Cfg.IsPhoneWhitelisted(phone) {
			skipped++
			continue
		}

		// Idempotencia (N-37): si ya existe un pending para este teléfono (recordatorio ya enviado
		// hoy; restaurado desde BD tras un reinicio), no reenviar. Hace seguro el re-run del
		// catch-up cuando una corrida previa quedó cortada a media lista: notifica solo a los que
		// faltaban, sin duplicar. En la corrida normal de las 07:00 aún no hay pending para la cita
		// de mañana, así que no altera el flujo establecido.
		if t.NotifyManager != nil && t.NotifyManager.HasPending(phone) {
			skipped++
			slog.Debug("skip reminder - already pending (idempotent re-run)", "phone", utils.MaskPhone(phone))
			continue
		}

		proceduresText := buildReminderProcedures(ctx, group, t.ProcedureRepo)
		if groupRequiresCreatinine(ctx, group, t.ProcedureRepo) {
			proceduresText = appendCreatinineNotice(proceduresText)
		}

		appointmentDate := utils.FormatFriendlyDate(firstAppt.Date)
		appointmentTime := services.FormatTimeSlot(firstAppt.TimeSlot)

		// Send confirmation template
		tmpl := bird.TemplateConfig{
			ProjectID: t.Cfg.BirdTemplateConfirmProjectID,
			VersionID: t.Cfg.BirdTemplateConfirmVersionID,
			Locale:    t.Cfg.BirdTemplateConfirmLocale,
			Params: []bird.TemplateParam{
				{Type: "string", Key: "patient_name", Value: firstAppt.PatientName},
				{Type: "string", Key: "clinic_name", Value: t.Cfg.CenterName},
				{Type: "string", Key: "appointment_date", Value: appointmentDate},
				{Type: "string", Key: "appointment_time", Value: appointmentTime},
				{Type: "string", Key: "procedures", Value: proceduresText},
			},
		}

		msgID, err := t.BirdClient.SendTemplate(phone, tmpl)
		if err != nil {
			slog.Error("send reminder failed", "phone", utils.MaskPhone(phone), "error", err)
			continue
		}
		slog.Info("reminder template sent", "phone", utils.MaskPhone(phone), "bird_msg_id", msgID)

		// Try to get conversationID for Bird Inbox visibility
		convID := t.BirdClient.GetCachedConversationID(phone)
		if convID == "" {
			convID, _ = t.BirdClient.LookupConversationByPhone(phone)
		}

		// Register pending notification with 6h timer
		t.NotifyManager.RegisterPending(notifications.PendingNotification{
			Type:           "confirmation",
			Phone:          phone,
			AppointmentID:  firstAppt.ID,
			BirdMessageID:  msgID,
			ConversationID: convID,
		})

		// Log event
		if t.Tracker != nil {
			t.Tracker.LogEvent(ctx, "", phone, "notification_sent",
				map[string]interface{}{
					"type":            "confirmation",
					"appointment_id":  firstAppt.ID,
					"bird_msg_id":     msgID,
					"conversation_id": convID,
				})
		}
		observability.Emit(observability.TraceNotif(firstAppt.ID), "notif_recordatorio", "reminder_sent",
			observability.EmitOpts{Phone: phone, RefID: msgID})

		// par.8.1 #8: preparacion del examen (si el catalogo la tiene) como texto aparte.
		t.sendReminderPrep(ctx, phone, convID, firstAppt.ID, group)

		sent++

		// Rate limiting: 2 seconds between sends (respeta cancelación)
		if err := sleepWithContext(ctx, 2*time.Second); err != nil {
			break
		}
	}

	slog.Info("whatsapp reminders complete", "sent", sent, "skipped", skipped)
	return nil
}

// === Task horaria: recordatorio de CORTA ANTELACIÓN (§8.1 #2) ===

// sameDayWindowCandidates filtra las citas de HOY cuya hora cae en [now+1h, now+2h): cada corrida
// horaria capta así cada cita exactamente una vez, ~1-2h antes de su hora. Excluye canceladas, ya
// confirmadas y TimeSlot no parseable (formato YYYYMMDDHHmm). Función pura para testear la ventana.
func sameDayWindowCandidates(now time.Time, appts []domain.Appointment) []domain.Appointment {
	lo := now.Add(1 * time.Hour)
	hi := now.Add(2 * time.Hour)
	var out []domain.Appointment
	for _, a := range appts {
		if a.Canceled || a.Confirmed {
			continue
		}
		slot, err := time.ParseInLocation("200601021504", a.TimeSlot, now.Location())
		if err != nil {
			continue
		}
		if !slot.Before(lo) && slot.Before(hi) {
			out = append(out, a)
		}
	}
	return out
}

// sendSameDayReminders envía el recordatorio a citas de HOY que nunca recibieron el del día antes
// (agendadas después de la corrida de las 07:00). Es ADITIVO al flujo existente: misma plantilla,
// mismo mecanismo de respuesta (RegisterPending), pero de un solo disparo (SameDay=true → al vencer
// el timer se limpia en silencio, sin followups ni escalación; ver handleConfirmationTimeout).
// Dedup fail-closed: sin NotifHistory (o si falla la consulta) NO se envía — jamás un doble recordatorio.
func (t *Tasks) sendSameDayReminders(ctx context.Context) error {
	if t.Cfg != nil && (!t.Cfg.WhatsAppNotificationsEnabled || !t.Cfg.SameDayRemindersEnabled) {
		slog.Debug("same-day reminders skipped — disabled by config")
		return nil
	}
	if t.NotifHistory == nil {
		slog.Warn("same-day reminders skipped — NotifHistory not wired (fail-closed, no dedup possible)")
		return nil
	}

	now := time.Now()
	today := now.Format("2006-01-02")

	appointments, err := t.AppointmentRepo.FindPendingByDate(ctx, today)
	if err != nil {
		return fmt.Errorf("fetch today appointments: %w", err)
	}

	candidates := sameDayWindowCandidates(now, appointments)
	if len(candidates) == 0 {
		return nil
	}
	patientGroups := groupAppointmentsByPatient(candidates)
	slog.Info("same-day reminders", "date", today, "window_slots", len(candidates), "patients", len(patientGroups))

	sent := 0
	skipped := 0

	for _, group := range patientGroups {
		if ctx.Err() != nil {
			break
		}
		firstAppt := group[0]

		phone := utils.ParseColombianPhone(firstAppt.PatientPhone)
		if phone == "" {
			skipped++
			continue
		}
		if !t.Cfg.IsPhoneWhitelisted(phone) {
			skipped++
			continue
		}
		// Ya hay un pending vivo para este teléfono (p.ej. otro recordatorio en curso) → no duplicar.
		if t.NotifyManager != nil && t.NotifyManager.HasPending(phone) {
			skipped++
			continue
		}
		// Dedup contra el recordatorio de las 07:00 (o una corrida horaria previa): si la cita ya fue
		// notificada, no se reenvía. Ante error de consulta, fail-closed (mejor no molestar).
		notified, err := t.NotifHistory.WasAppointmentNotified(ctx, firstAppt.ID)
		if err != nil {
			skipped++
			slog.Warn("same-day reminder skipped - dedup check failed (fail-closed)",
				"appointment_id", firstAppt.ID, "error", err)
			continue
		}
		if notified {
			skipped++
			continue
		}

		proceduresText := buildReminderProcedures(ctx, group, t.ProcedureRepo)
		if groupRequiresCreatinine(ctx, group, t.ProcedureRepo) {
			proceduresText = appendCreatinineNotice(proceduresText)
		}
		tmpl := bird.TemplateConfig{
			ProjectID: t.Cfg.BirdTemplateConfirmProjectID,
			VersionID: t.Cfg.BirdTemplateConfirmVersionID,
			Locale:    t.Cfg.BirdTemplateConfirmLocale,
			Params: []bird.TemplateParam{
				{Type: "string", Key: "patient_name", Value: firstAppt.PatientName},
				{Type: "string", Key: "clinic_name", Value: t.Cfg.CenterName},
				{Type: "string", Key: "appointment_date", Value: utils.FormatFriendlyDate(firstAppt.Date)},
				{Type: "string", Key: "appointment_time", Value: services.FormatTimeSlot(firstAppt.TimeSlot)},
				{Type: "string", Key: "procedures", Value: proceduresText},
			},
		}

		msgID, err := t.BirdClient.SendTemplate(phone, tmpl) //nolint:contextcheck // SendTemplate no toma ctx (cliente Bird; mismo patrón que sendWhatsAppReminders)
		if err != nil {
			slog.Error("same-day reminder send failed", "phone", utils.MaskPhone(phone), "error", err)
			continue
		}

		convID := t.BirdClient.GetCachedConversationID(phone)
		if convID == "" {
			convID, _ = t.BirdClient.LookupConversationByPhone(phone)
		}

		//nolint:contextcheck // RegisterPending no toma ctx por diseño (crea su propio timeout acotado; mismo patrón que waiting_list_check)
		t.NotifyManager.RegisterPending(notifications.PendingNotification{
			Type:           "confirmation",
			Phone:          phone,
			AppointmentID:  firstAppt.ID,
			BirdMessageID:  msgID,
			ConversationID: convID,
			SameDay:        true,
		})

		if t.Tracker != nil {
			t.Tracker.LogEvent(ctx, "", phone, "notification_sent",
				map[string]interface{}{
					"type":            "confirmation_same_day",
					"appointment_id":  firstAppt.ID,
					"bird_msg_id":     msgID,
					"conversation_id": convID,
				})
		}
		observability.Emit(observability.TraceNotif(firstAppt.ID), "notif_recordatorio", "same_day_reminder_sent",
			observability.EmitOpts{Phone: phone, RefID: msgID})

		t.sendReminderPrep(ctx, phone, convID, firstAppt.ID, group)

		sent++
		if err := sleepWithContext(ctx, 2*time.Second); err != nil {
			break
		}
	}

	slog.Info("same-day reminders complete", "sent", sent, "skipped", skipped)
	// Corte por ctx cancelado (apagado) es salida limpia, no fallo — mismo patrón que sendWhatsAppReminders.
	return nil //nolint:nilerr
}

// prepTextForGroup arma el texto de PREPARACION de los procedimientos de la cita (par.8.1 #8:
// suenio/EEG/RX tienen 15-20% de no-show; recordar la preparacion reduce inasistencia y estudios
// fallidos). Toma la preparacion del catalogo local; "" si ninguno la tiene. Tope rune-safe.
func prepTextForGroup(ctx context.Context, group []domain.Appointment, procRepo repository.ProcedureRepository) string {
	if procRepo == nil {
		return ""
	}
	seen := map[string]bool{}
	var parts []string
	for _, a := range group {
		for _, p := range a.Procedures {
			if p.CupCode == "" || seen[p.CupCode] {
				continue
			}
			seen[p.CupCode] = true
			proc, err := procRepo.FindByCode(ctx, p.CupCode)
			if err == nil && proc != nil && strings.TrimSpace(proc.Preparation) != "" {
				parts = append(parts, strings.TrimSpace(proc.Preparation))
			}
		}
	}
	if len(parts) == 0 {
		return ""
	}
	txt := "📋 *Preparacion para tu examen:*\n" + strings.Join(parts, "\n")
	if r := []rune(txt); len(r) > 900 {
		txt = string(r[:900]) + "..."
	}
	return txt
}

// sendReminderPrep envia la preparacion como texto aparte tras la plantilla (la plantilla es fija).
func (t *Tasks) sendReminderPrep(ctx context.Context, phone, convID, apptID string, group []domain.Appointment) {
	prep := prepTextForGroup(ctx, group, t.ProcedureRepo)
	if prep == "" {
		return
	}
	if _, err := t.BirdClient.SendText(phone, convID, prep); err != nil { //nolint:contextcheck // cliente Bird sin ctx (patron existente)
		slog.Warn("reminder prep send failed", "phone", utils.MaskPhone(phone), "error", err)
		return
	}
	if t.Tracker != nil {
		t.Tracker.LogEvent(ctx, "", phone, "reminder_prep_sent", map[string]interface{}{"appointment_id": apptID})
	}
}

// sendMorningReengagement (par.8.1 #9): un mensaje a quienes rebotaron fuera de horario ayer tarde.
func (t *Tasks) sendMorningReengagement(ctx context.Context) error {
	if t.Cfg != nil && (!t.Cfg.WhatsAppNotificationsEnabled || !t.Cfg.MorningReengageEnabled) {
		return nil
	}
	if t.ReengageSource == nil {
		return nil
	}
	phones, err := t.ReengageSource.PhonesForMorningReengage(ctx)
	if err != nil {
		return fmt.Errorf("reengage phones: %w", err)
	}
	sent := 0
	for _, phone := range phones {
		if ctx.Err() != nil {
			break
		}
		ph := utils.ParseColombianPhone(phone)
		if ph == "" || !t.Cfg.IsPhoneWhitelisted(ph) {
			continue
		}
		convID := t.BirdClient.GetCachedConversationID(ph)
		//nolint:contextcheck // cliente Bird sin ctx (patron existente)
		if _, err := t.BirdClient.SendText(ph, convID,
			"¡Buenos dias! 😊 Ya estamos disponibles. ¿Te ayudo a agendar tu cita? Escribeme *hola* para comenzar."); err != nil {
			slog.Warn("morning reengage send failed", "phone", utils.MaskPhone(ph), "error", err)
			continue
		}
		if t.Tracker != nil {
			t.Tracker.LogEvent(ctx, "", ph, "reengagement_sent", nil)
		}
		sent++
		if err := sleepWithContext(ctx, 2*time.Second); err != nil {
			break
		}
	}
	slog.Info("morning reengagement complete", "candidates", len(phones), "sent", sent)
	return nil //nolint:nilerr // corte por ctx cancelado = salida limpia
}

// === Task 15:00: Voice Reminders (IVR) ===
//
// Cambio 14: Uses escalation chain instead of broken !HasPending filter.
// Calls patients who completed WA follow-up chain (RetryCount==2),
// then sets post-IVR timer for final agent escalation.

func (t *Tasks) sendVoiceReminders(ctx context.Context) error {
	if t.Cfg != nil && !t.Cfg.IVRNotificationsEnabled {
		slog.Info("voice reminders skipped — IVR notifications disabled (IVR_NOTIFICATIONS_ENABLED=false)")
		return nil
	}

	// Get patients ready for IVR: RetryCount==0 (followup disabled) or RetryCount==2 (followup enabled)
	targets := t.NotifyManager.GetPendingForIVR()
	if len(targets) == 0 {
		slog.Info("voice reminders: no targets")
		return nil
	}

	// Get tomorrow's appointments for IVR call parameters
	tomorrow := time.Now().AddDate(0, 0, 1).Format("2006-01-02")
	appointments, err := t.AppointmentRepo.FindPendingByDate(ctx, tomorrow)
	if err != nil {
		return fmt.Errorf("fetch appointments for IVR: %w", err)
	}

	// Build phone → appointment map for quick lookup
	apptByPhone := make(map[string]domain.Appointment)
	for _, appt := range appointments {
		phone := utils.ParseColombianPhone(appt.PatientPhone)
		if phone != "" {
			apptByPhone[phone] = appt
		}
	}

	sent := 0
	for _, pending := range targets {
		// Detener llamadas si el contexto fue cancelado (apagado)
		if ctx.Err() != nil {
			break
		}
		if !t.Cfg.IsPhoneWhitelisted(pending.Phone) {
			continue
		}

		appt, ok := apptByPhone[pending.Phone]
		if !ok {
			continue // No matching appointment — may have been confirmed/cancelled meanwhile
		}

		// Resolve address from first procedure in the appointment (via center_location_id → center_locations)
		clinicAddress := ""
		if t.ProcedureRepo != nil {
			for _, proc := range appt.Procedures {
				if proc.CupCode == "" {
					continue
				}
				if p, err := t.ProcedureRepo.FindByCode(ctx, utils.BaseCupCode(proc.CupCode)); err == nil && p != nil && p.Address != "" {
					clinicAddress = p.Address
					break
				}
			}
		}

		callID, err := t.BirdClient.PlaceCall(pending.Phone, map[string]string{
			"patient_name":     appt.PatientName,
			"appointment_date": utils.FormatFriendlyDate(appt.Date),
			"appointment_time": services.FormatTimeSlot(appt.TimeSlot),
			"clinic_name":      t.Cfg.CenterName,
			"clinic_address":   clinicAddress,
		})
		if err != nil {
			slog.Error("voice call failed", "phone", utils.MaskPhone(pending.Phone), "error", err)
			continue
		}

		// Register callId → phone mapping for DTMF webhook correlation
		if callID != "" {
			t.NotifyManager.RegisterCallID(callID, pending.Phone)
		}

		// Mark IVR sent: stops old safety-net timer, sets retry=3, new post-IVR timer
		t.NotifyManager.MarkIVRSent(pending.Phone)

		if t.Tracker != nil {
			t.Tracker.LogEvent(ctx, "", pending.Phone, "notification_ivr_sent",
				map[string]interface{}{"appointment_id": pending.AppointmentID})
		}
		observability.Emit(observability.TraceNotif(pending.AppointmentID), "notif_recordatorio", "ivr_placed",
			observability.EmitOpts{Phone: pending.Phone, RefID: callID})

		slog.Info("ivr call initiated", "phone", utils.MaskPhone(pending.Phone), "appointment_id", pending.AppointmentID)

		sent++
		// Rate limit for calls (respeta cancelación)
		if err := sleepWithContext(ctx, 3*time.Second); err != nil {
			break
		}
	}

	slog.Info("voice reminders complete", "sent", sent, "targets", len(targets))
	return nil
}

// === Task 02:00: Cleanup ===

func (t *Tasks) cleanup(ctx context.Context) error {
	// Note: session cleanup is handled by StartInactivityChecker (Fase 20)

	// Expire old waiting list entries
	if t.WaitingListRepo != nil {
		wlExpired, err := t.WaitingListRepo.ExpireOld(ctx, 30)
		if err != nil {
			slog.Error("waiting list cleanup error", "error", err)
		} else if wlExpired > 0 {
			slog.Info("waiting list entries expired by cleanup", "count", wlExpired)
		}

		// Expirar ofertas sin respuesta > 24h (notified pero no agendó): se liberan para no bloquear
		// al paciente ni re-notificarle antes de responder; la oferta se re-evalúa para otros.
		staleExpired, err := t.WaitingListRepo.ExpireStaleNotified(ctx, 24)
		if err != nil {
			slog.Error("waiting list stale-notified cleanup error", "error", err)
		} else if staleExpired > 0 {
			slog.Info("waiting list stale notified expired", "count", staleExpired)
		}
	}

	// Clean up processed inbox messages older than 24h (WAL)
	if t.InboxRepo != nil {
		deleted, err := t.InboxRepo.DeleteOlderThan(ctx, 24)
		if err != nil {
			slog.Error("inbox cleanup error", "error", err)
		} else if deleted > 0 {
			slog.Info("inbox messages cleaned", "deleted", deleted)
		}
	}

	// Borrar historial de notificaciones > 45 días (notification_history) para que no crezca.
	if t.NotifyManager != nil {
		n, err := t.NotifyManager.CleanupHistory(ctx, 45)
		if err != nil {
			slog.Error("notification history cleanup error", "error", err)
		} else if n > 0 {
			slog.Info("notification history cleaned", "deleted", n)
		}
	}

	// Reconciliación de invariantes (Fase 2 observabilidad): cruza acciones del bot vs estado real
	// sobre la ventana reciente y emite anomalías consultables (/anomalies) + alerta Telegram.
	if t.Reconciler != nil {
		t.Reconciler.Run(ctx)
	}

	// Rollup del día anterior + purga de crudos > 45 días (Fase 3 observabilidad): los agregados
	// quedan en flow_daily_stats (sobreviven a la purga) y la tabla de crudos no crece.
	if t.FlowMaint != nil {
		if _, err := t.FlowMaint.RollupDay(ctx, time.Now().AddDate(0, 0, -1)); err != nil {
			slog.Error("flow rollup error", "error", err)
		}
		if purged, err := t.FlowMaint.PurgeOlderThan(ctx, 45); err != nil {
			slog.Error("flow purge error", "error", err)
		} else if purged > 0 {
			slog.Info("flow_events purged", "deleted", purged)
		}
	}

	return nil
}

// === Task 08:00: Waiting List Check ===

func (t *Tasks) checkWaitingList(ctx context.Context) error {
	if t.WaitingListRepo == nil {
		slog.Debug("waiting list check: dependencies not available")
		return nil
	}

	// 1. Expirar entries > 30 días
	expired, err := t.WaitingListRepo.ExpireOld(ctx, 30)
	if err != nil {
		slog.Error("expire waiting list", "error", err)
	}
	if expired > 0 {
		slog.Info("waiting list entries expired", "count", expired)
	}

	// 2. Obtener CUPS distintos con entries en estado "waiting"
	cupsCodes, err := t.WaitingListRepo.GetDistinctWaitingCups(ctx)
	if err != nil {
		return fmt.Errorf("get waiting cups: %w", err)
	}

	if len(cupsCodes) == 0 {
		slog.Debug("waiting list check: no waiting entries")
		return nil
	}

	totalNotified := 0

	// El match oferta↔demanda (FIFO con skip + capacidad + claim-then-send) vive en
	// NotifyManager.CheckWaitingListForCups, compartido con el flujo en tiempo real (al liberarse un
	// cupo). La tarea diaria solo lo invoca por cada CUP en espera.
	if t.NotifyManager == nil {
		slog.Warn("waiting list check: NotifyManager no disponible")
		return nil
	}
	for _, cupsCode := range cupsCodes {
		if ctx.Err() != nil {
			break
		}
		totalNotified += t.NotifyManager.CheckWaitingListForCups(ctx, cupsCode)
		// Rate limit entre CUPS (respeta cancelación).
		if err := sleepWithContext(ctx, 2*time.Second); err != nil {
			return err
		}
	}

	slog.Info("waiting list check complete", "cups_checked", len(cupsCodes), "notified", totalNotified)
	return nil
}

// === Helpers ===

// sleepWithContext espera la duración indicada o retorna antes si el contexto se cancela.
func sleepWithContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// buildReminderProcedures arma el texto de procedimientos del recordatorio. Si el paciente tiene
// VARIAS citas ese día (a horas distintas) incluye la hora de cada una, porque el param
// appointment_time del template solo refleja una; con una sola cita deja solo el nombre (la hora va
// en appointment_time). CupName puede venir igual al código si el catálogo no está cargado: en ese
// caso resuelve el nombre real vía procRepo. Rune-safe y acotado al límite del body de WhatsApp.
// groupRequiresCreatinine indica si alguna cita del grupo es CONTRASTADA (requiere creatinina vigente),
// combinando la observación de SIESA con el nombre del CUP (mismo criterio que al reprogramar). Se usa
// para agregar el aviso de creatinina en el recordatorio de WhatsApp.
func groupRequiresCreatinine(ctx context.Context, group []domain.Appointment, procRepo repository.ProcedureRepository) bool {
	for _, appt := range group {
		var names []string
		for _, proc := range appt.Procedures {
			name := proc.CupName
			if (name == "" || name == proc.CupCode) && procRepo != nil {
				if p, err := procRepo.FindByCode(ctx, utils.BaseCupCode(proc.CupCode)); err == nil && p != nil && p.Name != "" {
					name = p.Name
				}
			}
			names = append(names, name)
		}
		if utils.AppointmentIsContrasted(appt.Observations, names...) {
			return true
		}
	}
	return false
}

// appendCreatinineNotice agrega el aviso de creatinina al texto de procedimientos del recordatorio. Va
// EN UNA SOLA LÍNEA porque WhatsApp no admite saltos de línea en los parámetros de plantilla.
func appendCreatinineNotice(proceduresText string) string {
	return proceduresText + ". IMPORTANTE: por ser un examen con contraste, debes llevar tu examen de creatinina vigente (máximo 30 días de tomado)"
}

func buildReminderProcedures(ctx context.Context, group []domain.Appointment, procRepo repository.ProcedureRepository) string {
	multiAppt := len(group) > 1
	var procedures []string
	for _, appt := range group {
		seen := make(map[string]bool)
		var names []string
		for _, proc := range appt.Procedures {
			code := proc.CupCode
			if code == "" || seen[code] {
				continue
			}
			seen[code] = true
			name := proc.CupName
			if (name == "" || name == code) && procRepo != nil {
				if p, err := procRepo.FindByCode(ctx, utils.BaseCupCode(code)); err == nil && p != nil && p.Name != "" {
					name = p.Name
				}
			}
			if name == "" {
				name = code
			}
			names = append(names, name)
		}
		if len(names) == 0 {
			names = []string{"Procedimiento"}
		}
		entry := strings.Join(names, ", ")
		if multiAppt {
			entry += " (" + services.FormatTimeSlot(appt.TimeSlot) + ")"
		}
		procedures = append(procedures, entry)
	}
	if len(procedures) == 0 {
		return "Procedimiento"
	}
	text := strings.Join(procedures, " y ")
	if r := []rune(text); len(r) > 600 { // límite del body del template de WhatsApp (rune-safe)
		text = string(r[:599]) + "…"
	}
	return text
}

func groupAppointmentsByPatient(appointments []domain.Appointment) map[string][]domain.Appointment {
	groups := make(map[string][]domain.Appointment)
	for _, appt := range appointments {
		groups[appt.PatientID] = append(groups[appt.PatientID], appt)
	}
	return groups
}
