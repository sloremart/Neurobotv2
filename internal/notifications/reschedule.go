package notifications

import (
	"context"
	"log/slog"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/utils"
)

// handleReschedule processes responses to the reschedule notification template.
// Uses the same session-based flows as handleConfirmation (confirm/reschedule/cancel).
func (m *NotificationManager) handleReschedule(phone, action string, pending *PendingNotification) {
	m.handleConfirmation(phone, action, pending)
}

// handleCancellation processes responses to the agenda cancellation template.
// NOTE: Caller (HandleResponse) already removed pending from sync.Map via LoadAndDelete.
func (m *NotificationManager) handleCancellation(phone, action string, pending *PendingNotification) {
	// L13: ctx acotado a 30s (patrón N-46). Sin deadline, un LogEvent colgado bloquearía la goroutine
	// indefinidamente mientras sostiene el phoneLock del paciente.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	switch action {
	case "acknowledge": // postback: "understood"
		m.birdClient.SendText(phone, pending.ConversationID,
			"Entendido. Lamentamos los inconvenientes. "+
				"Si necesitas reagendar tu cita, puedes escribirnos cuando gustes.")

		if pending.ConversationID != "" {
			m.birdClient.CloseFeedItems(pending.ConversationID)
		}

		if m.tracker != nil {
			elapsed := time.Since(pending.CreatedAt).Minutes()
			m.tracker.LogEvent(ctx, "", phone, "notification_cancel_acknowledged", map[string]interface{}{
				"appointment_id":    pending.AppointmentID,
				"response_time_min": int(elapsed),
				"conversation_id":   pending.ConversationID,
			})
		}

		slog.Info("cancellation acknowledged", "phone", utils.MaskPhone(phone), "appointment_id", pending.AppointmentID)

	case "reschedule": // postback: "reprogramar" or "reschedule"
		m.startSelfReschedule(phone, pending, true) // skipCancel=true: already cancelled by admin
	}
}
