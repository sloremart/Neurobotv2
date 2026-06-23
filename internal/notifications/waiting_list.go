package notifications

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/neuro-bot/neuro-bot/internal/session"
	"github.com/neuro-bot/neuro-bot/internal/utils"
)

// handleWaitingList processes responses to the waiting list availability template.
func (m *NotificationManager) handleWaitingList(phone, action string, pending *PendingNotification) {
	ctx := context.Background()

	switch action {
	case "schedule": // postback: "wl_schedule"
		entry, err := m.waitingListRepo.FindByID(ctx, pending.WaitingListID)
		if err != nil || entry == nil {
			slog.Error("waiting list: find entry", "error", err, "entry_id", pending.WaitingListID)
			return
		}

		// Crear sesión nueva con contexto pre-cargado
		sess := &session.Session{
			ID:           uuid.New().String(),
			PhoneNumber:  phone,
			CurrentState: "SEARCH_SLOTS",
			Status:       session.StatusActive,
			ExpiresAt:    time.Now().Add(120 * time.Minute),
		}

		sessionCtx := map[string]string{
			"patient_id":            entry.PatientID,
			"patient_doc":           entry.PatientDoc,
			"patient_name":          entry.PatientName,
			"patient_age":           fmt.Sprintf("%d", entry.PatientAge),
			"patient_gender":        entry.PatientGender,
			"patient_entity":        entry.PatientEntity,
			"patient_contract":      entry.ContractCode, // MRC contract — keeps WL booking consistent with the main flow (citas.contrato + MRC limit)
			"cups_code":             entry.CupsCode,
			"cups_name":             entry.CupsName,
			"is_contrasted":         boolToStr(entry.IsContrasted),
			"is_sedated":            boolToStr(entry.IsSedated),
			"espacios":              fmt.Sprintf("%d", entry.Espacios),
			"procedures_json":       entry.ProceduresJSON,
			"total_procedures":      "1",
			"current_procedure_idx": "0",
			"menu_option":           "agendar",
			"waiting_list_entry_id": entry.ID,
		}

		// GFR data
		if entry.GfrCreatinine > 0 {
			sessionCtx["gfr_creatinine"] = fmt.Sprintf("%.2f", entry.GfrCreatinine)
			sessionCtx["gfr_height_cm"] = fmt.Sprintf("%d", entry.GfrHeightCm)
			sessionCtx["gfr_weight_kg"] = fmt.Sprintf("%.1f", entry.GfrWeightKg)
			sessionCtx["gfr_disease_type"] = entry.GfrDiseaseType
			sessionCtx["gfr_calculated"] = fmt.Sprintf("%.1f", entry.GfrCalculated)
		}

		// Extras
		if entry.IsPregnant {
			sessionCtx["is_pregnant"] = "1"
		}
		if entry.BabyWeightCat != "" {
			sessionCtx["baby_weight_cat"] = entry.BabyWeightCat
		}
		if entry.PreferredDoctorDoc != "" {
			sessionCtx["preferred_doctor_doc"] = entry.PreferredDoctorDoc
		}

		// Crear sesión + contexto en BD
		if err := m.sessionRepo.Create(ctx, sess); err != nil {
			slog.Error("waiting list: create session", "error", err)
			return
		}

		if err := m.sessionRepo.SetContextBatch(ctx, sess.ID, sessionCtx); err != nil {
			slog.Error("waiting list: set context", "error", err)
			m.sessionRepo.UpdateStatus(ctx, sess.ID, session.StatusCompleted) // cleanup orphan
			return
		}

		m.birdClient.SendText(phone, pending.ConversationID,
			"Vamos a buscar horarios disponibles para *"+entry.CupsName+"*...")

		// Encolar mensaje virtual para que el worker pool ejecute SEARCH_SLOTS
		m.workerPool.EnqueueVirtual(phone)

		if m.tracker != nil {
			m.tracker.LogEvent(ctx, sess.ID, phone, "waiting_list_schedule_accepted", map[string]interface{}{
				"waiting_list_id": pending.WaitingListID,
				"cups_code":       entry.CupsCode,
			})
		}

		slog.Info("waiting list session created",
			"phone", utils.MaskPhone(phone),
			"entry_id", entry.ID,
			"cups_code", entry.CupsCode)

	case "decline": // postback: "wl_decline"
		m.waitingListRepo.UpdateStatus(ctx, pending.WaitingListID, "declined")

		m.birdClient.SendText(phone, pending.ConversationID,
			"Entendido. Si cambias de opinión, puedes escribirnos para agendar tu cita.")

		if pending.ConversationID != "" {
			m.birdClient.CloseFeedItems(pending.ConversationID)
		}

		if m.tracker != nil {
			m.tracker.LogEvent(ctx, "", phone, "waiting_list_schedule_declined", map[string]interface{}{
				"waiting_list_id": pending.WaitingListID,
			})
		}

		slog.Info("waiting list declined", "phone", utils.MaskPhone(phone), "entry_id", pending.WaitingListID)
	}
}

// handleWaitingListTimeout handles the 6-hour no-response case for waiting list.
// Unlike confirmation, waiting list timeout does NOT retry.
func (m *NotificationManager) handleWaitingListTimeout(pending *PendingNotification) {
	ctx := context.Background()
	m.waitingListRepo.UpdateStatus(ctx, pending.WaitingListID, "expired")
	// Already removed from sync.Map by handleTimeout via LoadAndDelete
	slog.Info("waiting list notification expired", "phone", utils.MaskPhone(pending.Phone), "entry_id", pending.WaitingListID)
}

func boolToStr(b bool) string {
	if b {
		return "1"
	}
	return "0"
}
