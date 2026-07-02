package notifications

import (
	"context"
	"log/slog"
	"strconv"

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

	notified := m.notifyWaitingEntries(ctx, entries, remaining)
	if notified > 0 {
		slog.Info("wl_check: notifications sent", "cups_code", cupsCode, "notified", notified)
	}
	return notified
}

// CheckWaitingListForSlot empareja un SLOT liberado (médico + agenda) contra la lista de espera. Un
// slot no pertenece a un CUP sino a (médico + asuntos de su agenda): puede llenarlo cualquier CUP que
// el médico realice (cups_medico) cuyo asunto atienda esa agenda. Trae TODOS esos candidatos en FIFO
// y corre la misma validación por-entrada que el matching por CUP. Devuelve cuántos notificó. Se llama
// al liberarse un slot (cancelación). Requiere las deps de matching por slot (agenda/cups resolvers).
func (m *NotificationManager) CheckWaitingListForSlot(ctx context.Context, codMedi, agendaID int) int {
	if m.wlChecker == nil || m.slotSearcher == nil || m.apptChecker == nil {
		return 0
	}
	if m.agendaResolver == nil || m.cupsResolver == nil {
		return 0
	}
	if m.cfg == nil || m.cfg.BirdTemplateWaitingListProjectID == "" {
		return 0
	}

	asuntos, err := m.agendaResolver.GetAsuntosByAgenda(ctx, agendaID)
	if err != nil {
		slog.Error("wl_slot: get asuntos by agenda", "agenda_id", agendaID, "error", err)
		return 0
	}
	if len(asuntos) == 0 {
		slog.Info("wl_slot: agenda sin asuntos", "agenda_id", agendaID)
		return 0
	}

	// Conjunto elegible = CUPS del médico en esos asuntos. Se resuelve por asunto para poder tomar un
	// cup representativo por asunto y acotar la capacidad con pocas consultas (≤ nº de asuntos).
	var eligible, reps []string
	for _, a := range asuntos {
		cs, cerr := m.cupsResolver.FindCupsForDoctorAndAsuntos(ctx, codMedi, []int{a})
		if cerr != nil {
			slog.Error("wl_slot: eligible cups", "cod_medi", codMedi, "asunto", a, "error", cerr)
			continue
		}
		if len(cs) == 0 {
			continue
		}
		eligible = append(eligible, cs...)
		reps = append(reps, cs[0])
	}
	if len(eligible) == 0 {
		slog.Info("wl_slot: sin cups elegibles", "cod_medi", codMedi, "asuntos", asuntos)
		return 0
	}

	entries, err := m.wlChecker.GetWaitingByCupsIn(ctx, eligible, 200)
	if err != nil {
		slog.Error("wl_slot: get waiting by cups", "cod_medi", codMedi, "error", err)
		return 0
	}
	remaining := m.freeSlotCapacity(ctx, codMedi, reps)
	if remaining == 0 {
		slog.Info("wl_slot: sin capacidad libre", "cod_medi", codMedi, "agenda_id", agendaID)
		return 0
	}

	notified := m.notifyWaitingEntries(ctx, entries, remaining)
	if notified > 0 {
		slog.Info("wl_slot: notifications sent",
			"cod_medi", codMedi, "agenda_id", agendaID, "eligible_cups", len(eligible), "notified", notified)
	}
	return notified
}

// freeSlotCapacity cuenta los slots-unidad libres del médico codMedi a través de los CUPS
// representativos (uno por asunto), deduplicando por (agenda, día, hora). Es la cota para no
// sobre-notificar; reutiliza GetAvailableSlots (sin query nueva). Bounded por el nº de asuntos.
func (m *NotificationManager) freeSlotCapacity(ctx context.Context, codMedi int, repCups []string) int {
	codStr := strconv.Itoa(codMedi)
	seen := make(map[string]bool)
	for _, cup := range repCups {
		slots, err := m.slotSearcher.GetAvailableSlots(ctx, services.SlotQuery{CupsCode: cup, Espacios: 1, MaxSlots: 500})
		if err != nil {
			continue
		}
		for _, s := range slots {
			if s.DoctorSiesaCode != codStr {
				continue
			}
			seen[strconv.Itoa(s.AgendaID)+"|"+s.Date+"|"+s.TimeSlot] = true
		}
	}
	return len(seen)
}

// notifyWaitingEntries recorre entradas en FIFO y notifica a las que caben en la capacidad y tienen un
// bloque contiguo válido (con sus restricciones), en claim-then-send. Descuenta capacidad por notificado
// (FIFO con skip). Cada entrada se auto-describe por su CupsCode, así que sirve tanto al matching por
// CUP como al matching por SLOT. Devuelve cuántas notificó.
func (m *NotificationManager) notifyWaitingEntries(ctx context.Context, entries []domain.WaitingListEntry, remaining int) int {
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
		hasFuture, herr := m.apptChecker.HasFutureForCup(ctx, entry.PatientID, entry.CupsCode)
		if herr != nil {
			slog.Error("wl_check: has future check", "patient_id", entry.PatientID, "error", herr)
			continue
		}
		if hasFuture {
			if uerr := m.wlChecker.UpdateStatus(ctx, entry.ID, "duplicate_found"); uerr != nil {
				slog.Warn("wl_check: update duplicate status", "entry_id", entry.ID, "error", uerr)
			}
			slog.Info("wl_check: duplicate found", "entry_id", entry.ID, "cups_code", entry.CupsCode)
			observability.Emit(trace, "lista_espera", "duplicate_found",
				observability.EmitOpts{Phone: entry.PhoneNumber})
			continue
		}
		// ¿Existe un bloque contiguo del tamaño que requiere ESTA entrada, con sus restricciones?
		slots, serr := m.slotSearcher.GetAvailableSlots(ctx, m.entrySlotQuery(ctx, entry))
		if serr != nil {
			slog.Error("wl_check: get available slots", "cups_code", entry.CupsCode, "entry_id", entry.ID, "error", serr)
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
		if !m.sendWaitingNotification(ctx, entry) {
			if uerr := m.wlChecker.UpdateStatus(ctx, entry.ID, "waiting"); uerr != nil {
				slog.Warn("wl_check: revert claim to waiting", "entry_id", entry.ID, "error", uerr)
			}
			continue
		}
		remaining -= entry.Espacios
		notified++
	}
	return notified
}

// entrySlotQuery arma la consulta de slots para una entrada de lista de espera, con sus
// restricciones (espacios, contraste, sedación, médico preferido y tope mensual MRC).
func (m *NotificationManager) entrySlotQuery(ctx context.Context, entry domain.WaitingListEntry) services.SlotQuery {
	q := services.SlotQuery{
		CupsCode:     entry.CupsCode,
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
		if _, _, found := services.IsMRCGroupCups(entry.CupsCode); found {
			q.MonthFilter = func(year, month int) (bool, error) {
				blocked, err := m.apptSvc.CheckMRCLimitForMonth(ctx, entry.CupsCode, entry.ContractCode, 0, year, month)
				if err != nil {
					slog.Warn("wl_check: mrc month filter error (fail-open)", "cups_code", entry.CupsCode, "year", year, "month", month, "error", err)
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
func (m *NotificationManager) sendWaitingNotification(ctx context.Context, entry domain.WaitingListEntry) bool {
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

	// L12: si el pending NO se almacena (timeout del lock), la respuesta del paciente se perdería
	// (HandleResponse no encontraría pending). Devolver false → el caller revierte el claim a
	// 'waiting' para re-ofrecer la entrada con un pending rastreable.
	//nolint:contextcheck // RegisterPending no toma ctx por diseño (crea su propio timeout acotado para el Upsert; se llama igual desde webhooks/scheduler).
	if !m.RegisterPending(PendingNotification{
		Type:           "waiting_list",
		Phone:          entry.PhoneNumber,
		WaitingListID:  entry.ID,
		BirdMessageID:  msgID,
		ConversationID: convID,
	}) {
		observability.Emit(trace, "lista_espera", "notify_failed",
			observability.EmitOpts{Phone: entry.PhoneNumber, Reason: "register_pending_failed"})
		return false
	}

	if m.tracker != nil {
		m.tracker.LogEvent(ctx, "", entry.PhoneNumber, "notification_sent",
			map[string]interface{}{
				"type":            "waiting_list",
				"waiting_list_id": entry.ID,
				"cups_code":       entry.CupsCode,
				"bird_msg_id":     msgID,
				"conversation_id": convID,
			})
	}

	observability.Emit(trace, "lista_espera", "notified", observability.EmitOpts{
		Phone: entry.PhoneNumber, RefID: msgID,
		Attrs: map[string]interface{}{"cups": entry.CupsCode},
	})
	slog.Info("wl_check: notification sent",
		"phone", utils.MaskPhone(entry.PhoneNumber),
		"entry_id", entry.ID,
		"cups_code", entry.CupsCode)
	return true
}
