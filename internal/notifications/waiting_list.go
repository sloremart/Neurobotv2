package notifications

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/neuro-bot/neuro-bot/internal/observability"
	"github.com/neuro-bot/neuro-bot/internal/session"
	"github.com/neuro-bot/neuro-bot/internal/utils"
)

// handleWaitingList processes responses to the waiting list availability template.
func (m *NotificationManager) handleWaitingList(phone, action string, pending *PendingNotification) {
	// N-46: acotar el ctx para los writes a la BD local (Create/SetContextBatch/UpdateStatus).
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

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

		// Crear sesión + contexto en BD.
		//
		// H130-5: el cupo único del teléfono puede estar ocupado por una sesión FANTASMA (escalada que
		// nadie cerró y ya venció). Antes el Create fallaba con ErrActiveSessionExists, se logueaba el
		// error y el flujo moría en silencio: el paciente que acababa de pedir su cita desde la lista de
		// espera no recibía NADA. Se libera el cupo y se reintenta — salvo que quien lo ocupe sea una
		// escalación VIVA, porque ahí hay un agente humano en ese chat y no se le arrebata.
		if err := m.sessionRepo.Create(ctx, sess); err != nil {
			if !errors.Is(err, session.ErrActiveSessionExists) {
				slog.Error("waiting list: create session", "error", err)
				return
			}
			occupant, ferr := m.sessionRepo.FindCurrentByPhone(ctx, phone)
			if ferr != nil {
				slog.Error("waiting list: lookup session occupant", "error", ferr)
				return
			}
			if occupant != nil && occupant.Status == session.StatusEscalated &&
				occupant.ExpiresAt.After(time.Now()) {
				slog.Warn("waiting list: escalación viva ocupa el cupo, la agenda queda para el agente",
					"phone", utils.MaskPhone(phone), "session_id", occupant.ID)
				return
			}
			if cerr := m.sessionRepo.CompleteActiveByPhone(ctx, phone); cerr != nil {
				slog.Error("waiting list: liberar cupo de sesión fantasma", "error", cerr)
				return
			}
			if rerr := m.sessionRepo.Create(ctx, sess); rerr != nil {
				slog.Error("waiting list: create session tras liberar cupo", "error", rerr)
				return
			}
			slog.Info("waiting list: cupo liberado de sesión fantasma, sesión creada",
				"phone", utils.MaskPhone(phone), "session_id", sess.ID)
		}
		if m.tracker != nil { // sesión proactiva: contar también en total_sessions
			m.tracker.LogEvent(ctx, sess.ID, phone, "session_started", map[string]interface{}{"proactive": true})
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
		observability.Emit(observability.TraceWaitingList(pending.WaitingListID), "lista_espera",
			"response_schedule", observability.EmitOpts{Phone: phone, Attrs: map[string]interface{}{"cups": entry.CupsCode}})

		slog.Info("waiting list session created",
			"phone", utils.MaskPhone(phone),
			"entry_id", entry.ID,
			"cups_code", entry.CupsCode)

	case "decline": // postback: "wl_decline"
		// Solo declina si sigue 'notified': si el paciente ya agendó esa oferta desde el bot
		// (→ 'scheduled') no se debe degradar a 'declined'.
		if _, err := m.waitingListRepo.ResolveIfNotified(ctx, pending.WaitingListID, "declined"); err != nil {
			slog.Warn("waiting list: decline failed", "entry_id", pending.WaitingListID, "error", err)
		}

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
		observability.Emit(observability.TraceWaitingList(pending.WaitingListID), "lista_espera",
			"declined", observability.EmitOpts{Phone: phone})

		slog.Info("waiting list declined", "phone", utils.MaskPhone(phone), "entry_id", pending.WaitingListID)
	}
}

// handleWaitingListTimeout handles the 6-hour no-response case for waiting list.
// Unlike confirmation, waiting list timeout does NOT retry.
func (m *NotificationManager) handleWaitingListTimeout(pending *PendingNotification) {
	// N-46: acotar el ctx para el write a la BD local (UpdateStatus).
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// Solo expira si sigue 'notified': si el paciente agendó esa oferta desde el bot mientras el timer
	// seguía vivo (→ 'scheduled'), un expire incondicional pisaba ese estado y corrompía los KPIs.
	resolved, err := m.waitingListRepo.ResolveIfNotified(ctx, pending.WaitingListID, "expired")
	if err != nil {
		slog.Warn("waiting list: expire on timeout failed", "entry_id", pending.WaitingListID, "error", err)
		return
	}
	if !resolved {
		slog.Info("waiting list timeout skipped: entry no longer notified",
			"phone", utils.MaskPhone(pending.Phone), "entry_id", pending.WaitingListID)
		return
	}
	// Already removed from sync.Map by handleTimeout via LoadAndDelete
	observability.Emit(observability.TraceWaitingList(pending.WaitingListID), "lista_espera",
		"expired", observability.EmitOpts{Phone: pending.Phone, Reason: "timeout_6h"})
	slog.Info("waiting list notification expired", "phone", utils.MaskPhone(pending.Phone), "entry_id", pending.WaitingListID)
}

func boolToStr(b bool) string {
	if b {
		return "1"
	}
	return "0"
}
