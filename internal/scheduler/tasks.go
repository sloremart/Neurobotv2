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
	InboxRepo       InboxCleaner // WAL cleanup (optional)
}

// RegisterAll registers the 4 scheduled tasks.
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
		Fn: t.sendWhatsAppReminders,
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
		Fn: t.checkWaitingList,
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
		Fn: t.sendVoiceReminders,
	})
}

// === Task 07:00: WhatsApp Reminders ===

// SendWhatsAppReminders is the public entry point for manual/test triggers.
func (t *Tasks) SendWhatsAppReminders(ctx context.Context) error {
	return t.sendWhatsAppReminders(ctx)
}

func (t *Tasks) sendWhatsAppReminders(ctx context.Context) error {
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

		proceduresText := buildReminderProcedures(ctx, group, t.ProcedureRepo)

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

		sent++

		// Rate limiting: 2 seconds between sends (respeta cancelación)
		if err := sleepWithContext(ctx, 2*time.Second); err != nil {
			break
		}
	}

	slog.Info("whatsapp reminders complete", "sent", sent, "skipped", skipped)
	return nil
}

// === Task 15:00: Voice Reminders (IVR) ===
//
// Cambio 14: Uses escalation chain instead of broken !HasPending filter.
// Calls patients who completed WA follow-up chain (RetryCount==2),
// then sets post-IVR timer for final agent escalation.

func (t *Tasks) sendVoiceReminders(ctx context.Context) error {
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

		// Resolve address from first procedure in the appointment (cups_procedimientos.direccion)
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
