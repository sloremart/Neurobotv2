package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/observability"
	"github.com/neuro-bot/neuro-bot/internal/services"
	"github.com/neuro-bot/neuro-bot/internal/utils"
)

// Origen de una notificación de lista de espera (KPI recuperación de cupos cancelados). Viaja como
// `reason` en lista_espera/notified y booked (entra al rollup diario → sobrevive la purga de crudos)
// y como WLTrigger en el pending → contexto de sesión.
const (
	WLTriggerDailyCheck  = "daily_check"  // tarea diaria 06:00 (y chequeos manuales por CUP)
	WLTriggerSlotRelease = "slot_release" // disparo en tiempo real al liberarse un slot por cancelación
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

	notified := m.notifyWaitingEntries(ctx, entries, remaining, WLTriggerDailyCheck)
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

	notified := m.notifyWaitingEntries(ctx, entries, remaining, WLTriggerSlotRelease)
	if notified > 0 {
		slog.Info("wl_slot: notifications sent",
			"cod_medi", codMedi, "agenda_id", agendaID, "eligible_cups", len(eligible), "notified", notified)
	}
	// KPI recuperación de cupos: registrar el disparo por liberación (incluye los que notificaron 0,
	// p.ej. lista vacía) para tener el denominador "liberaciones que dispararon chequeo". Terminal
	// de un solo evento (outcome info): no deja trazas "estancadas" en la auditoría.
	observability.Emit(fmt.Sprintf("slotrel:%d:%d:%d", codMedi, agendaID, time.Now().UnixMilli()),
		"lista_espera", "slot_released", observability.EmitOpts{
			Attrs: map[string]interface{}{"cod_medi": codMedi, "agenda_id": agendaID, "notified": notified},
		})
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

// entryQueryKey identifica una consulta de slots por los campos de la entrada que la determinan
// (ver entrySlotQuery: CupsCode, edad, contraste, sedación, espacios, médico preferido, grupo de
// CUPS y — vía MonthFilter — el contrato MRC). Dos entradas con la misma clave producen la MISMA
// consulta, así que dentro de una corrida pueden compartir resultado.
func entryQueryKey(e domain.WaitingListEntry) string {
	return e.CupsCode + "|" + strconv.Itoa(e.PatientAge) + "|" +
		strconv.FormatBool(e.IsContrasted) + "|" + strconv.FormatBool(e.IsSedated) + "|" +
		strconv.Itoa(e.Espacios) + "|" + e.PreferredDoctorDoc + "|" + e.ContractCode + "|" +
		e.ProceduresJSON
}

// notifyWaitingEntries recorre entradas en FIFO y notifica a las que caben en la capacidad y tienen un
// bloque contiguo válido (con sus restricciones), en claim-then-send. Descuenta capacidad por notificado
// (FIFO con skip). Cada entrada se auto-describe por su CupsCode, así que sirve tanto al matching por
// CUP como al matching por SLOT. Devuelve cuántas notificó.
//
// Auditoría queries P2: hasta 200 entradas por corrida y muchas comparten CUP y restricciones →
// las consultas idénticas (slots y cita-futura) se MEMOIZAN dentro de la corrida. Es seguro:
// notificar no reserva cupos, así que el resultado de la misma consulta no cambia entre entradas.
func (m *NotificationManager) notifyWaitingEntries(ctx context.Context, entries []domain.WaitingListEntry, remaining int, trigger string) int {
	notified := 0
	slotMemo := make(map[string][]services.AvailableSlot)
	futureMemo := make(map[string]bool)
	for _, entry := range entries {
		if remaining < 1 {
			break
		}
		if !m.cfg.IsPhoneWhitelisted(entry.PhoneNumber) {
			continue
		}
		trace := observability.TraceWaitingList(entry.ID)
		// BLOQUEOS SANITAS (H146): el bot JAMÁS agenda bloqueos a contratos SANITAS — los gates de
		// búsqueda/creación lo cortarían de todas formas, así que OFRECER esta entrada sería pagar
		// un template condenado (principio de costo: ni un envío que no puede terminar en cita).
		// La entrada queda 'waiting' para gestión del agente; solo se emite el skip medible.
		if services.IsSanitasContract(entry.ContractCode) && entryHasBloqueo(entry) {
			observability.Emit(trace, "lista_espera", "skipped",
				observability.EmitOpts{Phone: entry.PhoneNumber, Reason: "bloqueo_sanitas"})
			continue
		}
		// FIFO con skip: si no cabe en la capacidad restante, intentar el siguiente (más chico).
		if entry.Espacios > remaining {
			observability.Emit(trace, "lista_espera", "skipped", observability.EmitOpts{
				Phone: entry.PhoneNumber, Reason: "too_big",
				Attrs: map[string]interface{}{"espacios": entry.Espacios, "remaining": remaining},
			})
			continue
		}
		// Ya tiene cita para este CUP → no notificar.
		futureKey := entry.PatientID + "|" + entry.CupsCode
		hasFuture, seen := futureMemo[futureKey]
		if !seen {
			var herr error
			hasFuture, herr = m.apptChecker.HasFutureForCup(ctx, entry.PatientID, entry.CupsCode)
			if herr != nil {
				slog.Error("wl_check: has future check", "patient_id", entry.PatientID, "error", herr)
				continue
			}
			futureMemo[futureKey] = hasFuture
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
		slotKey := entryQueryKey(entry)
		slots, seen := slotMemo[slotKey]
		if !seen {
			var serr error
			slots, serr = m.slotSearcher.GetAvailableSlots(ctx, m.entrySlotQuery(ctx, entry))
			if serr != nil {
				slog.Error("wl_check: get available slots", "cups_code", entry.CupsCode, "entry_id", entry.ID, "error", serr)
				continue
			}
			slotMemo[slotKey] = slots
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
		// Enviar; si falla TRANSITORIO se revierte a 'waiting' (el siguiente ciclo reintenta).
		// Un fallo PERMANENTE (4xx de Bird / identificador no contactable) queda 'unreachable':
		// devolverlo a 'waiting' sería pagar un envío condenado en cada ciclo, para siempre.
		if err := m.sendWaitingNotification(ctx, entry, trigger); err != nil {
			status := "waiting"
			if bird.IsPermanentSendError(err) || errors.Is(err, ErrSuppressedNoWhatsApp) {
				status = "unreachable"
				slog.Warn("wl_check: entrada no contactable (fallo permanente, sin reintentos)",
					"entry_id", entry.ID, "phone", utils.MaskPhone(entry.PhoneNumber), "error", err)
			}
			if uerr := m.wlChecker.UpdateStatus(ctx, entry.ID, status); uerr != nil {
				slog.Warn("wl_check: update status tras fallo de envio", "entry_id", entry.ID, "status", status, "error", uerr)
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
	// PARIDAD CON EL AGENDAMIENTO: validar la franja de contraste (incluida la regla de abdomen TAC,
	// que depende de TODOS los CUPS del grupo, no solo el representativo) y la intersección de médicos
	// sobre la cita completa. Sin esto, una cita en espera multi-CUP (p.ej. TAC tórax+abdomen
	// contrastado) podría ofrecerse un slot que el flujo de reserva rechazaría.
	if gc := groupCupsFromEntry(entry); len(gc) > 0 {
		q.GroupCups = gc
	}
	if entry.PreferredDoctorDoc != "" {
		q.PreferredDoctor = entry.PreferredDoctorDoc
	}
	if m.apptSvc != nil && services.IsMRCPatient(entry.ContractCode) {
		if _, _, found := services.IsMRCGroupCups(entry.CupsCode); found {
			q.MonthFilter = func(year, month int) (bool, error) {
				// Lista de espera = alta nueva (el paciente no tiene esta cita aún) → sin exclusión.
				blocked, err := m.apptSvc.CheckMRCLimitForMonth(ctx, entry.CupsCode, entry.ContractCode, 0, year, month, "")
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

// entryHasBloqueo indica si la cita en espera incluye algún CUP de BLOQUEO (el primario o
// cualquiera del ProceduresJSON). Entradas así pueden existir en el pool: auto-add tras
// cancelación admin de una cita creada por un agente, o entradas previas al gate de VALIDATE_OCR.
func entryHasBloqueo(entry domain.WaitingListEntry) bool {
	if services.IsBloqueoCups(entry.CupsCode) {
		return true
	}
	for _, c := range groupCupsFromEntry(entry) {
		if services.IsBloqueoCups(c) {
			return true
		}
	}
	return false
}

// groupCupsFromEntry reconstruye el conjunto de CUPS de la cita en espera desde ProceduresJSON (un
// []services.CUPSGroup, igual que persiste el agendamiento). Devuelve nil si el JSON está vacío o es
// de datos viejos, en cuyo caso GetAvailableSlots cae al CupsCode representativo (retrocompatible).
func groupCupsFromEntry(entry domain.WaitingListEntry) []string {
	var groups []services.CUPSGroup
	if err := json.Unmarshal([]byte(entry.ProceduresJSON), &groups); err != nil {
		return nil
	}
	seen := make(map[string]bool)
	var out []string
	for _, g := range groups {
		for _, c := range g.Cups {
			if c.Code != "" && !seen[c.Code] {
				seen[c.Code] = true
				out = append(out, c.Code)
			}
		}
	}
	return out
}

// errRegisterPendingFailed: el template salió pero el pending no se pudo almacenar (transitorio:
// el caller revierte el claim para re-ofrecer con un pending rastreable).
var errRegisterPendingFailed = errors.New("register pending failed")

// sendWaitingNotification envía el template de lista de espera, registra el pending (con su
// trigger de origen) y loguea el evento. Devuelve el error del envío (el caller decide revertir o
// marcar unreachable según su clase).
func (m *NotificationManager) sendWaitingNotification(ctx context.Context, entry domain.WaitingListEntry, trigger string) error {
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
	// Número con fallos de entrega confirmados: el template se cobraría sin llegar. Fallo
	// permanente → el caller marca la entrada 'unreachable' (fuera del pool diario).
	if !m.Deliverable(ctx, entry.PhoneNumber) {
		slog.Warn("wl_check: envío suprimido (entregas fallando)",
			"phone", utils.MaskPhone(entry.PhoneNumber), "entry_id", entry.ID)
		observability.Emit(trace, "lista_espera", "notify_failed",
			observability.EmitOpts{Phone: entry.PhoneNumber, Reason: "suppressed_no_whatsapp"})
		return ErrSuppressedNoWhatsApp
	}
	msgID, err := m.birdClient.SendTemplate(entry.PhoneNumber, tmpl)
	if err != nil {
		// H148: un contacto no alcanzable (username sin BSUID resoluble) es un RESULTADO
		// esperado del gate, no un fallo del servidor — va en WARN. Si sonara en ERROR, cada
		// corrida diaria de la lista de espera dispararía alertas Telegram por comportamiento
		// correcto (regla del ciclo 133). El flow_event de abajo lo deja igualmente medible.
		if errors.Is(err, bird.ErrNonContactable) {
			slog.Warn("wl_check: contacto no alcanzable, oferta suprimida (sin costo)",
				"phone", utils.MaskPhone(entry.PhoneNumber), "error", err)
		} else {
			slog.Error("wl_check: send template", "phone", utils.MaskPhone(entry.PhoneNumber), "error", err)
		}
		reason := ""
		if bird.IsPermanentSendError(err) {
			reason = "permanent"
		}
		observability.Emit(trace, "lista_espera", "notify_failed",
			observability.EmitOpts{Phone: entry.PhoneNumber, Reason: reason})
		return err
	}

	convID := m.birdClient.GetCachedConversationID(entry.PhoneNumber)
	if convID == "" {
		convID, _ = m.birdClient.LookupConversationByPhone(entry.PhoneNumber)
	}

	// L12: si el pending NO se almacena (timeout del lock), la respuesta del paciente se perdería
	// (HandleResponse no encontraría pending). Error transitorio → el caller revierte el claim a
	// 'waiting' para re-ofrecer la entrada con un pending rastreable.
	//nolint:contextcheck // RegisterPending no toma ctx por diseño (crea su propio timeout acotado para el Upsert; se llama igual desde webhooks/scheduler).
	if !m.RegisterPending(PendingNotification{
		Type:           "waiting_list",
		Phone:          entry.PhoneNumber,
		WaitingListID:  entry.ID,
		BirdMessageID:  msgID,
		ConversationID: convID,
		WLTrigger:      trigger,
	}) {
		observability.Emit(trace, "lista_espera", "notify_failed",
			observability.EmitOpts{Phone: entry.PhoneNumber, Reason: "register_pending_failed"})
		return errRegisterPendingFailed
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
		Phone: entry.PhoneNumber, RefID: msgID, Reason: trigger,
		Attrs: map[string]interface{}{"cups": entry.CupsCode},
	})
	slog.Info("wl_check: notification sent",
		"phone", utils.MaskPhone(entry.PhoneNumber),
		"entry_id", entry.ID,
		"cups_code", entry.CupsCode)
	return nil
}
