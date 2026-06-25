package notifications

import (
	"context"
	"log/slog"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/services"
	"github.com/neuro-bot/neuro-bot/internal/utils"
)

// CheckWaitingListForCups checks if there are waiting list entries for a given CUPS code
// and notifies the first patient in FIFO order via the WL template.
// Returns the number of patients notified (0 or 1).
// Called asynchronously after a cancellation frees a slot.
func (m *NotificationManager) CheckWaitingListForCups(ctx context.Context, cupsCode string) int {
	// Guard: dependencies
	if m.wlChecker == nil || m.slotSearcher == nil || m.apptChecker == nil {
		return 0
	}

	// Guard: template not configured
	if m.cfg.BirdTemplateWaitingListProjectID == "" {
		return 0
	}

	// 1. Get first FIFO waiting entry for this CUPS
	entries, err := m.wlChecker.GetWaitingByCups(ctx, cupsCode, 1)
	if err != nil {
		slog.Error("wl_check: get waiting entries", "cups_code", cupsCode, "error", err)
		return 0
	}
	if len(entries) == 0 {
		return 0
	}

	entry := entries[0]

	// Phone whitelist guard
	if m.cfg != nil && !m.cfg.IsPhoneWhitelisted(entry.PhoneNumber) {
		return 0
	}

	// 2. Check if patient already has a future appointment for this CUPS
	hasFuture, err := m.apptChecker.HasFutureForCup(ctx, entry.PatientID, cupsCode)
	if err != nil {
		slog.Error("wl_check: has future check", "patient_id", entry.PatientID, "error", err)
		return 0
	}
	if hasFuture {
		m.wlChecker.UpdateStatus(ctx, entry.ID, "duplicate_found")
		slog.Info("wl_check: duplicate found", "entry_id", entry.ID, "cups_code", cupsCode)
		return 0
	}

	// 3. Verify there are actually available slots
	query := services.SlotQuery{
		CupsCode:     cupsCode,
		PatientAge:   entry.PatientAge,
		IsContrasted: entry.IsContrasted,
		IsSedated:    entry.IsSedated,
		Espacios:     entry.Espacios,
		MaxSlots:     1,
	}
	if entry.PreferredDoctorDoc != "" {
		query.PreferredDoctor = entry.PreferredDoctorDoc
	}

	// MRC monthly limit filter: solo para pacientes Sanitas MRC (contratos 5/6)
	if m.apptSvc != nil && services.IsMRCPatient(entry.ContractCode) {
		if _, _, found := services.IsMRCGroupCups(cupsCode); found {
			query.MonthFilter = func(year, month int) (bool, error) {
				// quantity=0 → fallback al sufijo del CUP (waiting list no persiste la cantidad del OCR).
				blocked, err := m.apptSvc.CheckMRCLimitForMonth(ctx, cupsCode, entry.ContractCode, 0, year, month)
				if err != nil {
					slog.Warn("wl_check: mrc month filter error (fail-open)", "cups_code", cupsCode, "year", year, "month", month, "error", err)
					return true, nil // fail-open
				}
				return !blocked, nil
			}
		}
	}

	slots, err := m.slotSearcher.GetAvailableSlots(ctx, query)
	if err != nil {
		slog.Error("wl_check: get available slots", "cups_code", cupsCode, "error", err)
		return 0
	}
	if len(slots) == 0 {
		slog.Info("wl_check: no slots available despite cancellation", "cups_code", cupsCode)
		return 0
	}

	// 4. Send WL template
	tmpl := bird.TemplateConfig{
		ProjectID: m.cfg.BirdTemplateWaitingListProjectID,
		VersionID: m.cfg.BirdTemplateWaitingListVersionID,
		Locale:    m.cfg.BirdTemplateWaitingListLocale,
		Params: []bird.TemplateParam{
			{Type: "string", Key: "patient_name", Value: entry.PatientName},
			{Type: "string", Key: "procedure_name", Value: entry.CupsName},
			{Type: "string", Key: "cups_code", Value: entry.CupsCode},
			{Type: "string", Key: "clinic_name", Value: m.cfg.CenterName},
		},
	}

	msgID, err := m.birdClient.SendTemplate(entry.PhoneNumber, tmpl)
	if err != nil {
		slog.Error("wl_check: send template", "phone", utils.MaskPhone(entry.PhoneNumber), "error", err)
		return 0
	}

	// 5. Mark as notified (atomic — prevents double-notification on concurrent cancellations)
	if err := m.wlChecker.MarkNotified(ctx, entry.ID); err != nil {
		slog.Error("wl_check: mark notified", "entry_id", entry.ID, "error", err)
		// Template was sent, continue with registration
	}

	// 6. Look up conversation ID
	convID := m.birdClient.GetCachedConversationID(entry.PhoneNumber)
	if convID == "" {
		convID, _ = m.birdClient.LookupConversationByPhone(entry.PhoneNumber)
	}

	// 7. Register pending notification with 6h timeout
	m.RegisterPending(PendingNotification{
		Type:           "waiting_list",
		Phone:          entry.PhoneNumber,
		WaitingListID:  entry.ID,
		BirdMessageID:  msgID,
		ConversationID: convID,
	})

	// 8. Log event
	if m.tracker != nil {
		m.tracker.LogEvent(ctx, "", entry.PhoneNumber, "notification_sent",
			map[string]interface{}{
				"type":            "waiting_list",
				"trigger":         "realtime",
				"waiting_list_id": entry.ID,
				"cups_code":       cupsCode,
				"bird_msg_id":     msgID,
				"conversation_id": convID,
			})
	}

	slog.Info("wl_check: notification sent",
		"phone", utils.MaskPhone(entry.PhoneNumber),
		"entry_id", entry.ID,
		"cups_code", cupsCode,
		"trigger", "realtime")

	return 1
}
