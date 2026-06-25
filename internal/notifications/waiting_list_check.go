package notifications

import (
	"context"
	"log/slog"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/observability"
	"github.com/neuro-bot/neuro-bot/internal/services"
	"github.com/neuro-bot/neuro-bot/internal/utils"
)

// CheckWaitingListForCups empareja la oferta de cupos disponibles para un CUP contra la demanda de
// la lista de espera, en orden FIFO. Para cada entrada en espera: si su Espacios cabe en la
// capacidad restante y hay un bloque contiguo disponible (con sus restricciones — contraste,
// sedación, médico preferido, tope MRC), la reclama (claim-then-send, N-33) y la notifica; si no
// cabe, salta a la siguiente (FIFO con skip). No hay sobre-cupo: se descuenta la capacidad por cada
// notificado. Devuelve cuántos pacientes notificó. Usada en tiempo real (al liberarse un cupo) y por
// la tarea diaria.
func (m *NotificationManager) CheckWaitingListForCups(ctx context.Context, cupsCode string) int {
	if m.wlChecker == nil || m.slotSearcher == nil || m.apptChecker == nil {
		return 0
	}
	if m.cfg == nil || m.cfg.BirdTemplateWaitingListProjectID == "" {
		return 0
	}

	// Capacidad = total de slots-unidad libres para este CUP (cota para no sobre-notificar).
	capSlots, err := m.slotSearcher.GetAvailableSlots(ctx, services.SlotQuery{CupsCode: cupsCode, Espacios: 1, MaxSlots: 500})
	if err != nil {
		slog.Error("wl_check: capacity query", "cups_code", cupsCode, "error", err)
		return 0
	}
	remaining := len(capSlots)
	if remaining == 0 {
		slog.Info("wl_check: no slots available", "cups_code", cupsCode)
		return 0
	}

	// Se traen MÁS entradas que la capacidad: con FIFO-con-skip, algunas no caben (requieren más
	// slots que los disponibles) y hay que mirar más abajo en la fila para encontrar las que sí.
	// El loop corta al agotar la capacidad.
	entries, err := m.wlChecker.GetWaitingByCups(ctx, cupsCode, 200)
	if err != nil {
		slog.Error("wl_check: get waiting entries", "cups_code", cupsCode, "error", err)
		return 0
	}

	notified := 0
	for _, entry := range entries {
		if remaining < 1 {
			break
		}
		if !m.cfg.IsPhoneWhitelisted(entry.PhoneNumber) {
			continue
		}
		trace := observability.TraceWaitingList(entry.ID)
		// FIFO con skip: si no cabe en la capacidad restante, intentar el siguiente (más chico).
		if entry.Espacios > remaining {
			observability.Emit(trace, "lista_espera", "skipped", observability.EmitOpts{
				Phone: entry.PhoneNumber, Reason: "too_big",
				Attrs: map[string]interface{}{"espacios": entry.Espacios, "remaining": remaining},
			})
			continue
		}
		// Ya tiene cita para este CUP → no notificar.
		hasFuture, herr := m.apptChecker.HasFutureForCup(ctx, entry.PatientID, cupsCode)
		if herr != nil {
			slog.Error("wl_check: has future check", "patient_id", entry.PatientID, "error", herr)
			continue
		}
		if hasFuture {
			if uerr := m.wlChecker.UpdateStatus(ctx, entry.ID, "duplicate_found"); uerr != nil {
				slog.Warn("wl_check: update duplicate status", "entry_id", entry.ID, "error", uerr)
			}
			slog.Info("wl_check: duplicate found", "entry_id", entry.ID, "cups_code", cupsCode)
			observability.Emit(trace, "lista_espera", "duplicate_found",
				observability.EmitOpts{Phone: entry.PhoneNumber})
			continue
		}
		// ¿Existe un bloque contiguo del tamaño que requiere ESTA entrada, con sus restricciones?
		slots, serr := m.slotSearcher.GetAvailableSlots(ctx, m.entrySlotQuery(ctx, cupsCode, entry))
		if serr != nil {
			slog.Error("wl_check: get available slots", "cups_code", cupsCode, "entry_id", entry.ID, "error", serr)
			continue
		}
		if len(slots) == 0 {
			observability.Emit(trace, "lista_espera", "skipped",
				observability.EmitOpts{Phone: entry.PhoneNumber, Reason: "no_block"})
			continue // no hay bloque contiguo para esta entrada → siguiente (FIFO con skip)
		}
		observability.Emit(trace, "lista_espera", "slot_match", observability.EmitOpts{
			Phone: entry.PhoneNumber,
			Attrs: map[string]interface{}{"espacios": entry.Espacios, "remaining": remaining},
		})
		// Claim-then-send: reclamar SOLO si sigue en 'waiting' (evita doble notificación, N-33).
		claimed, cerr := m.wlChecker.MarkNotified(ctx, entry.ID)
		if cerr != nil {
			slog.Error("wl_check: claim entry", "entry_id", entry.ID, "error", cerr)
			continue
		}
		if !claimed {
			observability.Emit(trace, "lista_espera", "claim_lost",
				observability.EmitOpts{Phone: entry.PhoneNumber})
			continue // otra corrida concurrente ya la reclamó
		}
		// Enviar; si falla, revertir a 'waiting' para que se reintente.
		if !m.sendWaitingNotification(ctx, entry, cupsCode) {
			if uerr := m.wlChecker.UpdateStatus(ctx, entry.ID, "waiting"); uerr != nil {
				slog.Warn("wl_check: revert claim to waiting", "entry_id", entry.ID, "error", uerr)
			}
			continue
		}
		remaining -= entry.Espacios
		notified++
	}

	if notified > 0 {
		slog.Info("wl_check: notifications sent", "cups_code", cupsCode, "notified", notified)
	}
	return notified
}

// entrySlotQuery arma la consulta de slots para una entrada de lista de espera, con sus
// restricciones (espacios, contraste, sedación, médico preferido y tope mensual MRC).
func (m *NotificationManager) entrySlotQuery(ctx context.Context, cupsCode string, entry domain.WaitingListEntry) services.SlotQuery {
	q := services.SlotQuery{
		CupsCode:     cupsCode,
		PatientAge:   entry.PatientAge,
		IsContrasted: entry.IsContrasted,
		IsSedated:    entry.IsSedated,
		Espacios:     entry.Espacios,
		MaxSlots:     1,
	}
	if entry.PreferredDoctorDoc != "" {
		q.PreferredDoctor = entry.PreferredDoctorDoc
	}
	if m.apptSvc != nil && services.IsMRCPatient(entry.ContractCode) {
		if _, _, found := services.IsMRCGroupCups(cupsCode); found {
			q.MonthFilter = func(year, month int) (bool, error) {
				blocked, err := m.apptSvc.CheckMRCLimitForMonth(ctx, cupsCode, entry.ContractCode, 0, year, month)
				if err != nil {
					slog.Warn("wl_check: mrc month filter error (fail-open)", "cups_code", cupsCode, "year", year, "month", month, "error", err)
					return true, nil // fail-open
				}
				return !blocked, nil
			}
		}
	}
	return q
}

// sendWaitingNotification envía el template de lista de espera, registra el pending y loguea el
// evento. Devuelve false si el envío falló (el caller revierte el claim).
func (m *NotificationManager) sendWaitingNotification(ctx context.Context, entry domain.WaitingListEntry, cupsCode string) bool {
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
	trace := observability.TraceWaitingList(entry.ID)
	msgID, err := m.birdClient.SendTemplate(entry.PhoneNumber, tmpl)
	if err != nil {
		slog.Error("wl_check: send template", "phone", utils.MaskPhone(entry.PhoneNumber), "error", err)
		observability.Emit(trace, "lista_espera", "notify_failed", observability.EmitOpts{Phone: entry.PhoneNumber})
		return false
	}

	convID := m.birdClient.GetCachedConversationID(entry.PhoneNumber)
	if convID == "" {
		convID, _ = m.birdClient.LookupConversationByPhone(entry.PhoneNumber)
	}

	m.RegisterPending(PendingNotification{
		Type:           "waiting_list",
		Phone:          entry.PhoneNumber,
		WaitingListID:  entry.ID,
		BirdMessageID:  msgID,
		ConversationID: convID,
	})

	if m.tracker != nil {
		m.tracker.LogEvent(ctx, "", entry.PhoneNumber, "notification_sent",
			map[string]interface{}{
				"type":            "waiting_list",
				"waiting_list_id": entry.ID,
				"cups_code":       cupsCode,
				"bird_msg_id":     msgID,
				"conversation_id": convID,
			})
	}

	observability.Emit(trace, "lista_espera", "notified", observability.EmitOpts{
		Phone: entry.PhoneNumber, RefID: msgID,
		Attrs: map[string]interface{}{"cups": cupsCode},
	})
	slog.Info("wl_check: notification sent",
		"phone", utils.MaskPhone(entry.PhoneNumber),
		"entry_id", entry.ID,
		"cups_code", cupsCode)
	return true
}
