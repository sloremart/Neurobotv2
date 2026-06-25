package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/repository"
	"github.com/neuro-bot/neuro-bot/internal/services"
	"github.com/neuro-bot/neuro-bot/internal/session"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
	"github.com/neuro-bot/neuro-bot/internal/utils"
)

// currentProcQuantity devuelve la cantidad total del procedimiento (grupo) que se está agendando,
// leída de procedures_json + current_procedure_idx. La orden llega con el CUP base y la cantidad
// real (del OCR, notación "(#N)") vive en CUPSEntry.Quantity. Se suma sobre los CUPS del grupo
// (mín. 1). Se usa para que el tope mensual MRC sume la cantidad real de esta orden, no 1.
func currentProcQuantity(sess *session.Session) int {
	var groups []services.CUPSGroup
	if err := json.Unmarshal([]byte(sess.GetContext("procedures_json")), &groups); err != nil {
		return 1
	}
	idx, _ := strconv.Atoi(sess.GetContext("current_procedure_idx"))
	if idx < 0 || idx >= len(groups) {
		return 1
	}
	total := 0
	for _, c := range groups[idx].Cups {
		if c.Quantity > 0 {
			total += c.Quantity
		} else {
			total++
		}
	}
	if total < 1 {
		total = 1
	}
	return total
}

// WaitingListCreator is the interface needed by the OFFER_WAITING_LIST handler.
type WaitingListCreator interface {
	Create(ctx context.Context, entry *domain.WaitingListEntry) error
	HasActiveForPatientAndCups(ctx context.Context, patientID, cupsCode string) (bool, error)
}

// advanceToNextProcedure checks if there are more procedure groups to process.
// If yes, returns a result that transitions to the next group. If no, returns nil.
func advanceToNextProcedure(sess *session.Session) *sm.StateResult {
	totalProc, _ := strconv.Atoi(sess.GetContext("total_procedures"))
	currentIdx, _ := strconv.Atoi(sess.GetContext("current_procedure_idx"))

	if currentIdx+1 >= totalProc {
		return nil
	}

	nextIdx := currentIdx + 1
	var groups []services.CUPSGroup
	if err := json.Unmarshal([]byte(sess.GetContext("procedures_json")), &groups); err != nil {
		return nil
	}
	if nextIdx >= len(groups) || len(groups[nextIdx].Cups) == 0 {
		return nil
	}

	nextGroup := groups[nextIdx]
	r := sm.NewResult(sm.StateCheckSpecialCups).
		WithText(fmt.Sprintf("Ahora procesaremos el siguiente procedimiento:\n*%s*", nextGroup.Cups[0].Name)).
		WithContext("current_procedure_idx", fmt.Sprintf("%d", nextIdx)).
		WithContext("cups_code", nextGroup.Cups[0].Code).
		WithContext("cups_name", nextGroup.Cups[0].Name).
		WithContext("espacios", fmt.Sprintf("%d", nextGroup.Espacios)).
		WithClearCtx("is_contrasted", "is_sedated", "is_pregnant",
			"gfr_creatinine", "gfr_height_cm", "gfr_weight_kg",
			"gfr_disease_type", "gfr_calculated",
			"selected_slot_id", "available_slots_json", "slots_after_date",
			"preferred_doctor_doc", "ocr_is_sedated", "ocr_is_contrasted",
			"_prompted_contrast", "_prompted_sedation", "_prompted_pregnancy",
			"cups_preparation", "cups_video_url", "cups_audio_url",
			"alternative_cups_codes", "created_appointment_id")

	// Propagate OCR sedation/contrast detection for next group
	for _, c := range nextGroup.Cups {
		if c.IsSedated {
			r.WithContext("ocr_is_sedated", "1")
			break
		}
	}
	for _, c := range nextGroup.Cups {
		if c.IsContrasted {
			r.WithContext("ocr_is_contrasted", "1")
			break
		}
	}
	return r
}

// RegisterSlotHandlers registra los 8 handlers de búsqueda de slots y agendamiento (Fase 10).
func RegisterSlotHandlers(
	m *sm.Machine,
	slotSvc *services.SlotService,
	apptSvc *services.AppointmentService,
	procRepo repository.ProcedureRepository,
	priceRepo repository.PriceRepository,
	entityRepo repository.EntityRepository,
	waitingListRepo WaitingListCreator,
	addrMapper *services.AddressMapper,
	birdClient *bird.Client,
) {
	m.Register(sm.StateSearchSlots, searchSlotsHandler(slotSvc, apptSvc, procRepo, priceRepo, entityRepo))
	m.Register(sm.StateCoverageNoConvenio, coverageNoConvenioHandler())
	m.Register(sm.StateSlotSearchRetry, slotSearchRetryHandler())
	m.Register(sm.StateShowSlots, showSlotsHandler(addrMapper))
	m.Register(sm.StateNoSlotsAvailable, noSlotsHandler(waitingListRepo))
	m.Register(sm.StateOfferWaitingList, offerWaitingListHandler(waitingListRepo))
	m.RegisterWithConfig(sm.StateConfirmBooking, sm.HandlerConfig{
		InputType: sm.InputButton,
		Options:   []string{"booking_confirm", "booking_change"},
		RetryPrompt: func(sess *session.Session, result *sm.StateResult) {
			slot := findSelectedSlot(sess)
			if slot == nil {
				result.NextState = sm.StateSearchSlots
				result.Messages = []sm.OutboundMessage{&sm.TextMessage{Text: "Horario no encontrado. Buscando nuevos horarios..."}}
				result.ClearCtx = append(result.ClearCtx, "selected_slot_id", "available_slots_json")
				return
			}
			summary := buildBookingSummary(sess, slot, addrMapper)
			result.Messages = append(result.Messages, &sm.ButtonMessage{
				Text: summary,
				Buttons: []sm.Button{
					{Text: "Confirmar cita", Payload: "booking_confirm"},
					{Text: "Elegir otro horario", Payload: "booking_change"},
				},
			})
		},
		Handler: confirmBookingHandler(),
	})
	m.Register(sm.StateReconfirmBooking, reconfirmBookingHandler(addrMapper))
	m.Register(sm.StateCreateAppointment, createAppointmentHandler(apptSvc, priceRepo, entityRepo, procRepo))
	m.Register(sm.StateBookingSuccess, bookingSuccessHandler(addrMapper))
	m.Register(sm.StateBookingFailed, bookingFailedHandler())
}

// isCupCovered indica si el contrato del paciente tiene CONVENIO para el CUP: existe tarifa > 0
// en sis_proc_precios para (manual del contrato, CUP). Precio 0 o no encontrado = SIN convenio
// (regla de negocio confirmada). Usa el mismo origen de precio que el agendamiento (contrato del
// paciente, fix GAP-3). Devuelve error cuando el chequeo no se pudo completar — el caller hace
// fail-open (no bloquear el agendamiento por un fallo técnico).
func isCupCovered(ctx context.Context, priceRepo repository.PriceRepository, entityRepo repository.EntityRepository, sess *session.Session, cup string) (bool, error) {
	if priceRepo == nil || entityRepo == nil {
		return false, fmt.Errorf("repos no disponibles")
	}
	lookupCode := sess.GetContext("patient_contract")
	if lookupCode == "" {
		lookupCode = sess.GetContext("patient_entity")
	}
	if lookupCode == "" {
		return false, fmt.Errorf("sin contrato/entidad en sesión")
	}
	entityData, err := entityRepo.FindByCode(ctx, lookupCode)
	if err != nil {
		return false, fmt.Errorf("entidad: %w", err)
	}
	if entityData == nil || entityData.PriceType == "" {
		return false, fmt.Errorf("entidad sin manual")
	}
	price, err := priceRepo.FindPrice(ctx, cup, entityData.PriceType)
	if err != nil {
		return false, fmt.Errorf("findprice: %w", err)
	}
	return price != nil && *price > 0, nil
}

// COVERAGE_NO_CONVENIO (interactivo) — el contrato del paciente no cubre el CUP; ofrece continuar
// como particular (que sí tiene tarifa) o escalar a un agente.
func coverageNoConvenioHandler() sm.StateHandler {
	return func(_ context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		result, selected := sm.ValidateButtonResponse(sess, msg, "cov_particular", "cov_agente")
		if result != nil {
			return result, nil // input inválido (retry) o escalamiento, ya resuelto
		}
		if selected == "cov_agente" {
			return sm.NewResult(sm.StateEscalateToAgent).
				WithText("Te comunicamos con un agente para gestionar tu cita.").
				WithEvent("coverage_escalate_agent", nil), nil
		}
		// cov_particular → cambiar a PARTICULAR y re-buscar (el particular siempre tiene tarifa > 0,
		// así el gate no se vuelve a disparar). Se limpia patient_contract para que lookupContract
		// resuelva el contrato particular al agendar.
		return sm.NewResult(sm.StateSearchSlots).
			WithContext("patient_entity", particularEntityCode).
			WithClearCtx("patient_contract").
			WithText("Perfecto, continuamos como *particular*. Buscando horarios disponibles...").
			WithEvent("coverage_continue_particular", nil), nil
	}
}

// SEARCH_SLOTS (automático) — busca slots disponibles con todos los filtros.
func searchSlotsHandler(slotSvc *services.SlotService, apptSvc *services.AppointmentService, procRepo repository.ProcedureRepository, priceRepo repository.PriceRepository, entityRepo repository.EntityRepository) sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		cupsCode := sess.GetContext("cups_code")
		alternativeCodes := sess.GetContext("alternative_cups_codes")
		age, _ := strconv.Atoi(sess.GetContext("patient_age"))
		isContrasted := sess.GetContext("is_contrasted") == "1"
		isSedated := sess.GetContext("is_sedated") == "1"
		espacios, _ := strconv.Atoi(sess.GetContext("espacios"))
		if espacios == 0 {
			espacios = 1
		}

		// Gate de cobertura (solo en la primera búsqueda, no en paginación "ver más"): si el
		// contrato del paciente NO tiene convenio para el CUP (precio 0 o inexistente en
		// sis_proc_precios), no se busca slot ni se agenda; se ofrece particular o agente.
		// Fail-OPEN: si el chequeo falla por un error técnico, se continúa normal (no bloquear).
		if sess.GetContext("slots_after_date") == "" {
			if covered, checkErr := isCupCovered(ctx, priceRepo, entityRepo, sess, cupsCode); checkErr == nil && !covered {
				cupsName := sess.GetContext("cups_name")
				if cupsName == "" {
					cupsName = cupsCode
				}
				return sm.NewResult(sm.StateCoverageNoConvenio).
					WithButtons(
						fmt.Sprintf("Tu EPS/contrato *no tiene convenio* para *%s*. Puedes agendarlo como *particular*, que tiene un costo.\n\n¿Cómo deseas continuar?", cupsName),
						sm.Button{Text: "Continuar particular", Payload: "cov_particular"},
						sm.Button{Text: "Hablar con agente", Payload: "cov_agente"},
					).
					WithEvent("coverage_no_convenio", map[string]interface{}{"cup": cupsCode}), nil
			} else if checkErr != nil {
				slog.Warn("coverage_check_failed_fail_open", "cup", cupsCode, "error", checkErr)
			}
		}

		// Try to find slots with the primary CUPS code first, then alternatives
		cupsCodesToTry := []string{cupsCode}
		if alternativeCodes != "" {
			cupsCodesToTry = append(cupsCodesToTry, strings.Split(alternativeCodes, ",")...)
		}

		var slots []services.AvailableSlot
		var err error
		var successfulCupsCode string

		for _, code := range cupsCodesToTry {
			// Look up procedure details from Antares: address, preparation, video only.
			// Agenda type is determined from SIESA (AsuntoPctos) inside SlotService — not from Antares.
			var address string
			if procRepo != nil {
				proc, _ := procRepo.FindByCode(ctx, code)
				if proc != nil {
					address = proc.Address
					if proc.Preparation != "" {
						sess.SetContext("cups_preparation", proc.Preparation)
					}
					if proc.VideoURL != "" {
						sess.SetContext("cups_video_url", proc.VideoURL)
					}
					if proc.AudioURL != "" {
						sess.SetContext("cups_audio_url", proc.AudioURL)
					}
				}
			}

			// Bloqueo (053105): set preferred doctor from last neurology consultation
			// (890374/890274) if not already set by checkPriorConsultHandler.
			if code == "053105" && apptSvc != nil && sess.GetContext("preferred_doctor_doc") == "" {
				patientID := sess.GetContext("patient_id")
				if patientID != "" {
					lastDoc, err := apptSvc.FindLastDoctorForCups(ctx, patientID, []string{"890374", "890274"})
					if err != nil {
						slog.Warn("bloqueo_last_doctor_lookup_error", "patient_id", patientID, "error", err)
					} else if lastDoc != "" {
						sess.SetContext("preferred_doctor_doc", lastDoc)
						slog.Debug("bloqueo_preferred_doctor_set", "patient_id", patientID, "doctor_doc", lastDoc)
					}
				}
			}

			query := services.SlotQuery{
				CupsCode:        code,
				PatientAge:      age,
				IsContrasted:    isContrasted,
				IsSedated:       isSedated,
				Espacios:        espacios,
				PreferredDoctor: sess.GetContext("preferred_doctor_doc"),
				AfterDate:       sess.GetContext("slots_after_date"),
				MaxSlots:        5,
				ClinicAddress:   address,
			}

			// MRC monthly limit filter (MRC patient + mrcGroup CUPS)
			if sess.GetContext("mrc_limit_check") == "1" && apptSvc != nil {
				contract := sess.GetContext("patient_contract")
				qty := currentProcQuantity(sess)
				query.MonthFilter = func(year, month int) (bool, error) {
					blocked, err := apptSvc.CheckMRCLimitForMonth(ctx, code, contract, qty, year, month)
					if err != nil {
						return true, nil // fail-open
					}
					return !blocked, nil
				}
			}

			slots, err = slotSvc.GetAvailableSlots(ctx, query)
			if err == nil && len(slots) > 0 {
				successfulCupsCode = code
				slog.Debug("found_slots_with_alternative_code", "original", cupsCode, "used", code, "slots_found", len(slots))
				break
			}
		}

		// Fallback: if preferred_doctor was set but no slots found, retry without doctor restriction
		if len(slots) == 0 && sess.GetContext("preferred_doctor_doc") != "" {
			slog.Debug("slots_preferred_doctor_fallback", "cups_code", cupsCode, "preferred_doctor", sess.GetContext("preferred_doctor_doc"))
			for _, code := range cupsCodesToTry {
				var address string
				if procRepo != nil {
					proc, _ := procRepo.FindByCode(ctx, code)
					if proc != nil {
						address = proc.Address
					}
				}
				query := services.SlotQuery{
					CupsCode:      code,
					PatientAge:    age,
					IsContrasted:  isContrasted,
					IsSedated:     isSedated,
					Espacios:      espacios,
					AfterDate:     sess.GetContext("slots_after_date"),
					MaxSlots:      5,
					ClinicAddress: address,
				}
				if sess.GetContext("mrc_limit_check") == "1" && apptSvc != nil {
					contract := sess.GetContext("patient_contract")
					qty := currentProcQuantity(sess)
					query.MonthFilter = func(year, month int) (bool, error) {
						blocked, err := apptSvc.CheckMRCLimitForMonth(ctx, code, contract, qty, year, month)
						if err != nil {
							return true, nil
						}
						return !blocked, nil
					}
				}
				slots, err = slotSvc.GetAvailableSlots(ctx, query)
				if err == nil && len(slots) > 0 {
					successfulCupsCode = code
					slog.Debug("slots_found_without_preferred_doctor", "cups_code", code, "slots_found", len(slots))
					break
				}
			}
		}

		if err != nil {
			msg := "Hubo un problema al buscar horarios disponibles. ¿Qué deseas hacer?"
			eventType := "slot_search_error"
			if errors.Is(err, context.DeadlineExceeded) {
				msg = "Tardó demasiado buscar horarios. ¿Qué deseas hacer?"
				eventType = "slot_search_timeout"
			}
			return sm.NewResult(sm.StateSlotSearchRetry).
				WithButtons(msg,
					sm.Button{Text: "Intentar de nuevo", Payload: "retry"},
					sm.Button{Text: "Volver al menú", Payload: "menu"},
				).
				WithEvent(eventType, map[string]interface{}{"error": err.Error()}), nil
		}

		if len(slots) == 0 {
			return sm.NewResult(sm.StateNoSlotsAvailable).
				WithEvent("no_slots_found", map[string]interface{}{"cups_code": cupsCode}), nil
		}

		// Update cups_code in session to the one that actually found slots
		if successfulCupsCode != "" && successfulCupsCode != cupsCode {
			sess.SetContext("cups_code", successfulCupsCode)
		}

		slotsJSON, _ := json.Marshal(slots)
		cupsName := sess.GetContext("cups_name")

		slog.Debug("search_slots_saving_json", "slots_count", len(slots), "json_length", len(slotsJSON))

		// Build numbered text list for SHOW_SLOTS
		var msgText strings.Builder
		msgText.WriteString(fmt.Sprintf("Horarios disponibles para *%s*:\n\n", cupsName))

		for i, slot := range slots {
			optionNum := i + 1
			dateStr := utils.FormatFriendlyDateShortStr(slot.Date)
			doctorInfo := ""
			if slot.DoctorName != "" {
				doctorInfo = fmt.Sprintf(" con Dr. %s", slot.DoctorName)
			}
			msgText.WriteString(fmt.Sprintf("%d. %s a las %s%s\n", optionNum, dateStr, slot.TimeDisplay, doctorInfo))
		}

		if len(slots) >= 5 {
			msgText.WriteString(fmt.Sprintf("\n%d. Ver más horarios\n", len(slots)+1))
		}
		msgText.WriteString("\n💬 Escribe el número de tu opción:")

		result := sm.NewResult(sm.StateShowSlots).
			WithContext("available_slots_json", string(slotsJSON)).
			WithText(msgText.String()).
			WithEvent("slots_found", map[string]interface{}{"count": len(slots)})

		// Persist procedure metadata so it's available in later turns (confirm/success)
		if prep := sess.GetContext("cups_preparation"); prep != "" {
			result.WithContext("cups_preparation", prep)
		}
		if videoURL := sess.GetContext("cups_video_url"); videoURL != "" {
			result.WithContext("cups_video_url", videoURL)
		}
		if audioURL := sess.GetContext("cups_audio_url"); audioURL != "" {
			result.WithContext("cups_audio_url", audioURL)
		}
		if procType := sess.GetContext("procedure_type"); procType != "" {
			result.WithContext("procedure_type", procType)
		}

		return result, nil
	}
}

// SLOT_SEARCH_RETRY (interactivo) — ofrece reintentar búsqueda o volver al menú.
func slotSearchRetryHandler() sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		result, selected := sm.ValidateButtonResponse(sess, msg, "retry", "menu")
		if result != nil {
			if result.NextState == sm.StateEscalateToAgent {
				return result, nil
			}
			result.Messages = []sm.OutboundMessage{&sm.ButtonMessage{
				Text: "Hubo un problema al buscar horarios disponibles. ¿Qué deseas hacer?",
				Buttons: []sm.Button{
					{Text: "Intentar de nuevo", Payload: "retry"},
					{Text: "Volver al menú", Payload: "menu"},
				},
			}}
			return result, nil
		}

		if selected == "retry" {
			return sm.NewResult(sm.StateSearchSlots), nil
		}
		return sm.NewResult(sm.StateMainMenu), nil
	}
}

// SHOW_SLOTS (interactivo) — usuario selecciona un slot de la lista numerada.
func showSlotsHandler(addrMapper *services.AddressMapper) sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		input := strings.TrimSpace(msg.Text)

		// Parse number from input
		optionNum, err := strconv.Atoi(input)
		if err != nil || optionNum < 1 {
			return sm.NewResult(sess.CurrentState).
				WithText("Por favor escribe solo el número de tu opción (ejemplo: 1, 2, 3...)"), nil
		}

		// Load available slots
		var slots []services.AvailableSlot
		slotsJSON := sess.GetContext("available_slots_json")
		slog.Debug("show_slots_input", "input", input, "option_num", optionNum, "slots_json_length", len(slotsJSON))

		if err := json.Unmarshal([]byte(slotsJSON), &slots); err != nil {
			preview := slotsJSON
			if len(preview) > 100 {
				preview = preview[:100]
			}
			slog.Error("failed_to_unmarshal_slots", "error", err, "json_preview", preview, "phone", utils.MaskPhone(sess.PhoneNumber))
			return sm.NewResult(sm.StateSlotSearchRetry).
				WithButtons(
					"Hubo un error al cargar los horarios guardados. ¿Qué deseas hacer?",
					sm.Button{Text: "Buscar de nuevo", Payload: "retry"},
					sm.Button{Text: "Volver al menú", Payload: "menu"},
				).
				WithEvent("slots_unmarshal_error", map[string]interface{}{"error": err.Error()}), nil
		}

		slog.Debug("show_slots_parsed", "slots_count", len(slots), "option_num", optionNum)

		// Check if user wants "Ver más"
		if optionNum == len(slots)+1 {
			if len(slots) > 0 {
				lastSlot := slots[len(slots)-1]
				return sm.NewResult(sm.StateSearchSlots).
					WithContext("slots_after_date", lastSlot.Date).
					WithClearCtx("available_slots_json").
					WithEvent("more_slots_requested", nil), nil
			}
			return sm.NewResult(sm.StateSearchSlots).
				WithClearCtx("available_slots_json"), nil
		}

		// Validate selection
		if optionNum > len(slots) {
			return sm.NewResult(sess.CurrentState).
				WithText(fmt.Sprintf("Opción inválida. Por favor escribe un número entre 1 y %d", len(slots)+1)), nil
		}

		// Get selected slot (convert 1-based to 0-based index)
		selected := slots[optionNum-1]

		// Valid selection → show confirmation
		dateDisplay := selected.Date
		if dt, err := time.Parse("2006-01-02", selected.Date); err == nil {
			dateDisplay = utils.FormatFriendlyDate(dt)
		}

		summary := fmt.Sprintf("*Resumen de tu cita:*\n\n"+
			"Procedimiento: %s\n"+
			"Doctor: Dr. %s\n"+
			"Fecha: %s\n"+
			"Hora: %s",
			sess.GetContext("cups_name"),
			selected.DoctorName,
			dateDisplay,
			selected.TimeDisplay)

		if selected.ClinicAddress != "" {
			if addrMapper != nil {
				summary += "\n" + addrMapper.FormatAddress(selected.ClinicAddress)
			} else {
				summary += fmt.Sprintf("\nDirección: %s", selected.ClinicAddress)
			}
		}
		summary += "\n\n¿Confirmas esta cita?"

		return sm.NewResult(sm.StateConfirmBooking).
			WithContext("selected_slot_id", selected.TimeSlot).
			WithButtons(summary,
				sm.Button{Text: "Confirmar cita", Payload: "booking_confirm"},
				sm.Button{Text: "Elegir otro horario", Payload: "booking_change"},
			).
			WithEvent("slot_selected", map[string]interface{}{"time_slot": selected.TimeSlot}), nil
	}
}

// NO_SLOTS_AVAILABLE (automático) — no hay slots, ofrecer lista de espera.
// Cambio 12: reschedule_skip_cancel=="1" (admin cancellation) → auto-add to WL.
// Cambio 12b: reschedule_appt_id set + skip_cancel=="0" → appointment still active, no WL.
func noSlotsHandler(wlRepo WaitingListCreator) sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		cupsName := sess.GetContext("cups_name")

		// Cambio 12: Auto-add to WL when coming from admin cancellation reschedule
		if sess.GetContext("reschedule_skip_cancel") == "1" && wlRepo != nil {
			return autoAddToWaitingList(ctx, sess, wlRepo, cupsName)
		}

		// Cambio 12b: Self-reschedule from active appointment (confirmation/reschedule template).
		// The old appointment is still active → offer Confirmar/Cancelar (not WL).
		if sess.GetContext("reschedule_appt_id") != "" {
			return sm.NewResult(sm.StateNotifRescheduleFallback).
				WithButtons(
					fmt.Sprintf("No hay horarios disponibles para *%s* en otra fecha.\n\nTu cita original sigue vigente. ¿Qué deseas hacer?", cupsName),
					sm.Button{Text: "Confirmar cita", Payload: "confirm"},
					sm.Button{Text: "Reprogramar", Payload: "reschedule"},
					sm.Button{Text: "Cancelar cita", Payload: "cancel"},
				).
				WithEvent("no_slots_reschedule_active", map[string]interface{}{
					"cups_code":          sess.GetContext("cups_code"),
					"reschedule_appt_id": sess.GetContext("reschedule_appt_id"),
				}), nil
		}

		return sm.NewResult(sm.StateOfferWaitingList).
			WithButtons(
				fmt.Sprintf("No hay horarios disponibles para *%s*.\n\n¿Deseas que te avisemos cuando haya disponibilidad?", cupsName),
				sm.Button{Text: "Sí, avisarme", Payload: "wl_yes"},
				sm.Button{Text: "No, gracias", Payload: "wl_no"},
			).
			WithEvent("no_slots_available", map[string]interface{}{"cups_code": sess.GetContext("cups_code")}), nil
	}
}

// autoAddToWaitingList adds the patient to the waiting list without asking (cancellation flow).
func autoAddToWaitingList(ctx context.Context, sess *session.Session, wlRepo WaitingListCreator, cupsName string) (*sm.StateResult, error) {
	patientID := sess.GetContext("patient_id")
	cupsCode := sess.GetContext("cups_code")

	// Check for duplicate
	hasActive, err := wlRepo.HasActiveForPatientAndCups(ctx, patientID, cupsCode)
	if err == nil && hasActive {
		dupMsg := "No hay horarios disponibles para *" + cupsName + "*.\n\n" +
			"Ya tienes una inscripción activa en la lista de espera. " +
			"Te avisaremos por WhatsApp cuando haya disponibilidad."
		if next := advanceToNextProcedure(sess); next != nil {
			next.Messages = append([]sm.OutboundMessage{&sm.TextMessage{Text: dupMsg}}, next.Messages...)
			return next.WithEvent("waiting_list_auto_duplicate", map[string]interface{}{
				"cups_code":      cupsCode,
				"patient_id":     patientID,
				"next_procedure": true,
			}), nil
		}
		return buildAutoCloseResult(dupMsg).
			WithEvent("waiting_list_auto_duplicate", map[string]interface{}{
				"cups_code":  cupsCode,
				"patient_id": patientID,
			}), nil
	}

	age, _ := strconv.Atoi(sess.GetContext("patient_age"))
	espacios, _ := strconv.Atoi(sess.GetContext("espacios"))
	if espacios == 0 {
		espacios = 1
	}

	entry := &domain.WaitingListEntry{
		ID:             uuid.New().String(),
		PhoneNumber:    sess.PhoneNumber,
		PatientID:      patientID,
		PatientDoc:     sess.GetContext("patient_doc"),
		PatientName:    sess.GetContext("patient_name"),
		PatientAge:     age,
		PatientGender:  sess.GetContext("patient_gender"),
		PatientEntity:  sess.GetContext("patient_entity"),
		ContractCode:   sess.GetContext("patient_contract"),
		CupsCode:       cupsCode,
		CupsName:       cupsName,
		IsContrasted:   sess.GetContext("is_contrasted") == "1",
		IsSedated:      sess.GetContext("is_sedated") == "1",
		Espacios:       espacios,
		ProceduresJSON: sess.GetContext("procedures_json"),
		ProcedureType:  sess.GetContext("procedure_type"),
		Status:         "waiting",
		ExpiresAt:      time.Now().AddDate(0, 0, 30),
	}

	entry.PreferredDoctorDoc = sess.GetContext("preferred_doctor_doc")

	if err := wlRepo.Create(ctx, entry); err != nil {
		slog.Error("auto_add_wl: create entry", "error", err, "phone", utils.MaskPhone(sess.PhoneNumber))
		return sm.NewResult(sm.StateMainMenu).
			WithText("No hay horarios disponibles para *"+cupsName+"*.\n\n"+
				"No pudimos inscribirte en la lista de espera en este momento. "+
				"Puedes intentarlo nuevamente desde el menú principal.").
			WithEvent("waiting_list_auto_failed", map[string]interface{}{
				"error": err.Error(),
			}), nil
	}

	autoMsg := "No hay horarios disponibles para *" + cupsName + "*.\n\n" +
		"Te hemos inscrito automáticamente en la *lista de espera*.\n" +
		"Te avisaremos por WhatsApp cuando haya disponibilidad.\n\n" +
		"La inscripción es válida por 30 días."

	if next := advanceToNextProcedure(sess); next != nil {
		next.Messages = append([]sm.OutboundMessage{&sm.TextMessage{Text: autoMsg}}, next.Messages...)
		return next.WithContext("waiting_list_entry_id", entry.ID).
			WithEvent("waiting_list_auto_joined", map[string]interface{}{
				"cups_code":      cupsCode,
				"patient_id":     patientID,
				"entry_id":       entry.ID,
				"next_procedure": true,
			}), nil
	}

	return buildAutoCloseResult(autoMsg).
		WithContext("waiting_list_entry_id", entry.ID).
		WithEvent("waiting_list_auto_joined", map[string]interface{}{
			"cups_code":  cupsCode,
			"patient_id": patientID,
			"entry_id":   entry.ID,
		}), nil
}

// OFFER_WAITING_LIST (interactivo) — usuario decide unirse o no a la lista de espera.
func offerWaitingListHandler(wlRepo WaitingListCreator) sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		result, selected := sm.ValidateButtonResponse(sess, msg, "wl_yes", "wl_no")
		if result != nil {
			if result.NextState == sm.StateEscalateToAgent {
				return result, nil
			}
			cupsName := sess.GetContext("cups_name")
			result.Messages = []sm.OutboundMessage{&sm.ButtonMessage{
				Text: fmt.Sprintf("No hay horarios disponibles para *%s*.\n\n¿Deseas que te avisemos cuando haya disponibilidad?", cupsName),
				Buttons: []sm.Button{
					{Text: "Sí, avisarme", Payload: "wl_yes"},
					{Text: "No, gracias", Payload: "wl_no"},
				},
			}}
			return result, nil
		}

		switch selected {
		case "wl_yes":
			patientID := sess.GetContext("patient_id")
			cupsCode := sess.GetContext("cups_code")
			cupsName := sess.GetContext("cups_name")

			// Verificar duplicado
			if wlRepo != nil {
				hasActive, err := wlRepo.HasActiveForPatientAndCups(ctx, patientID, cupsCode)
				if err == nil && hasActive {
					dupMsg := "Ya tienes una inscripción activa en la lista de espera para *" + cupsName + "*.\nTe avisaremos cuando haya disponibilidad."
					if next := advanceToNextProcedure(sess); next != nil {
						next.Messages = append([]sm.OutboundMessage{&sm.TextMessage{Text: dupMsg}}, next.Messages...)
						return next.WithEvent("waiting_list_duplicate", map[string]interface{}{
							"cups_code":      cupsCode,
							"patient_id":     patientID,
							"next_procedure": true,
						}), nil
					}
					return buildAutoCloseResult(dupMsg).
						WithEvent("waiting_list_duplicate", map[string]interface{}{
							"cups_code":  cupsCode,
							"patient_id": patientID,
						}), nil
				}
			}

			// Crear entry desde session context
			age, _ := strconv.Atoi(sess.GetContext("patient_age"))
			espacios, _ := strconv.Atoi(sess.GetContext("espacios"))
			if espacios == 0 {
				espacios = 1
			}

			entry := &domain.WaitingListEntry{
				ID:             uuid.New().String(),
				PhoneNumber:    sess.PhoneNumber,
				PatientID:      patientID,
				PatientDoc:     sess.GetContext("patient_doc"),
				PatientName:    sess.GetContext("patient_name"),
				PatientAge:     age,
				PatientGender:  sess.GetContext("patient_gender"),
				PatientEntity:  sess.GetContext("patient_entity"),
				ContractCode:   sess.GetContext("patient_contract"),
				CupsCode:       cupsCode,
				CupsName:       cupsName,
				IsContrasted:   sess.GetContext("is_contrasted") == "1",
				IsSedated:      sess.GetContext("is_sedated") == "1",
				Espacios:       espacios,
				ProceduresJSON: sess.GetContext("procedures_json"),
				ProcedureType:  sess.GetContext("procedure_type"),
				Status:         "waiting",
				ExpiresAt:      time.Now().AddDate(0, 0, 30),
			}

			// GFR data (si aplica)
			if gfr := sess.GetContext("gfr_creatinine"); gfr != "" {
				entry.GfrCreatinine, _ = strconv.ParseFloat(gfr, 64)
				entry.GfrHeightCm, _ = strconv.Atoi(sess.GetContext("gfr_height_cm"))
				entry.GfrWeightKg, _ = strconv.ParseFloat(sess.GetContext("gfr_weight_kg"), 64)
				entry.GfrDiseaseType = sess.GetContext("gfr_disease_type")
				entry.GfrCalculated, _ = strconv.ParseFloat(sess.GetContext("gfr_calculated"), 64)
			}

			// Extras
			entry.IsPregnant = sess.GetContext("is_pregnant") == "1"
			entry.BabyWeightCat = sess.GetContext("baby_weight_cat")
			entry.PreferredDoctorDoc = sess.GetContext("preferred_doctor_doc")

			// Guardar en BD
			if wlRepo != nil {
				if err := wlRepo.Create(ctx, entry); err != nil {
					return sm.NewResult(sm.StateMainMenu).
						WithText("No pudimos inscribirte en la lista de espera en este momento. "+
							"Puedes intentarlo nuevamente desde el menú principal.").
						WithEvent("waiting_list_creation_failed", map[string]interface{}{
							"error": err.Error(),
						}), nil
				}
			}

			wlMsg := "Te hemos inscrito en la *lista de espera*.\n\n" +
				"Te enviaremos un mensaje de WhatsApp cuando haya disponibilidad para *" + cupsName + "*.\n\n" +
				"La inscripción es válida por 30 días."

			if next := advanceToNextProcedure(sess); next != nil {
				next.Messages = append([]sm.OutboundMessage{&sm.TextMessage{Text: wlMsg}}, next.Messages...)
				return next.WithContext("waiting_list_entry_id", entry.ID).
					WithEvent("waiting_list_joined", map[string]interface{}{
						"cups_code":      cupsCode,
						"patient_id":     patientID,
						"entry_id":       entry.ID,
						"next_procedure": true,
					}), nil
			}

			return buildAutoCloseResult(wlMsg).
				WithContext("waiting_list_entry_id", entry.ID).
				WithEvent("waiting_list_joined", map[string]interface{}{
					"cups_code":  cupsCode,
					"patient_id": patientID,
					"entry_id":   entry.ID,
				}), nil

		case "wl_no":
			if next := advanceToNextProcedure(sess); next != nil {
				return next.WithEvent("waiting_list_declined", map[string]interface{}{
					"cups_code":      sess.GetContext("cups_code"),
					"next_procedure": true,
				}), nil
			}
			return buildAutoCloseResult("Entendido. No te inscribimos en la lista de espera.").
				WithEvent("waiting_list_declined", map[string]interface{}{
					"cups_code": sess.GetContext("cups_code"),
				}), nil
		}

		return nil, fmt.Errorf("unreachable")
	}
}

// CONFIRM_BOOKING — solo lógica de negocio (validación declarativa en RegisterWithConfig).
func confirmBookingHandler() sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		selected := sm.ValidatedPayload(ctx)

		switch selected {
		case "booking_confirm":
			confirmMsg := "¿Estás seguro de *confirmar* esta cita?"
			if sess.GetContext("reschedule_appt_id") != "" {
				confirmMsg = "⚠️ Al confirmar, tu cita actual será *cancelada* y se asignará este nuevo horario.\n\n¿Deseas continuar con la reprogramación?"
			}
			return sm.NewResult(sm.StateReconfirmBooking).
				WithButtons(confirmMsg,
					sm.Button{Text: "Sí, confirmar", Payload: "reconfirm_yes"},
					sm.Button{Text: "No, volver", Payload: "reconfirm_no"},
				).
				WithEvent("booking_reconfirm_requested", nil), nil

		case "booking_change":
			return sm.NewResult(sm.StateSearchSlots).
				WithClearCtx("selected_slot_id", "available_slots_json", "slots_after_date").
				WithEvent("booking_change_requested", nil), nil
		}

		return nil, fmt.Errorf("unreachable")
	}
}

// RECONFIRM_BOOKING (interactivo) — segunda confirmación antes de crear la cita.
func reconfirmBookingHandler(addrMapper *services.AddressMapper) sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		result, selected := sm.ValidateButtonResponse(sess, msg, "reconfirm_yes", "reconfirm_no")
		if result != nil {
			if result.NextState == sm.StateEscalateToAgent {
				return result, nil
			}
			result.Messages = nil
			return sm.NewResult(sess.CurrentState).
				WithButtons("¿Estás seguro de *confirmar* esta cita?",
					sm.Button{Text: "Sí, confirmar", Payload: "reconfirm_yes"},
					sm.Button{Text: "No, volver", Payload: "reconfirm_no"},
				), nil
		}

		switch selected {
		case "reconfirm_yes":
			slog.Info("reconfirm_yes_received",
				"session_id", sess.ID,
				"phone", utils.MaskPhone(sess.PhoneNumber),
				"selected_slot_id", sess.GetContext("selected_slot_id"),
			)
			return sm.NewResult(sm.StateCreateAppointment).
				WithEvent("booking_confirmed", nil), nil

		case "reconfirm_no":
			// Reschedule: volver al menú principal de notificación
			if sess.GetContext("reschedule_appt_id") != "" {
				return sm.NewResult(sm.StateNotifPending).
					WithButtons(
						fmt.Sprintf("Entendido, tu cita actual no será modificada.\n\n📅 *Fecha:* %s\n🕐 *Hora:* %s\n💊 *Procedimiento:* %s\n\n¿Qué deseas hacer?",
							sess.GetContext("notif_appt_date"), sess.GetContext("notif_appt_time"), sess.GetContext("notif_cups_name")),
						sm.Button{Text: "Confirmar cita", Payload: "confirm"},
						sm.Button{Text: "Reprogramar", Payload: "reschedule"},
						sm.Button{Text: "Cancelar cita", Payload: "cancel"},
					).
					WithClearCtx("selected_slot_id", "available_slots_json", "slots_after_date").
					WithEvent("reschedule_declined_at_reconfirm", nil), nil
			}
			// Cita nueva: volver al resumen con Confirmar/Elegir otro
			slot := findSelectedSlot(sess)
			if slot == nil {
				return sm.NewResult(sm.StateSearchSlots).
					WithText("Horario no encontrado. Buscando nuevos horarios...").
					WithClearCtx("selected_slot_id", "available_slots_json"), nil
			}
			summary := buildBookingSummary(sess, slot, addrMapper)
			return sm.NewResult(sm.StateConfirmBooking).
				WithButtons(summary,
					sm.Button{Text: "Confirmar cita", Payload: "booking_confirm"},
					sm.Button{Text: "Elegir otro horario", Payload: "booking_change"},
				), nil
		}

		return nil, fmt.Errorf("unreachable: selected=%s", selected)
	}
}

// CREATE_APPOINTMENT (automático) — crea la cita en la BD externa.
func createAppointmentHandler(apptSvc *services.AppointmentService, priceRepo repository.PriceRepository, entityRepo repository.EntityRepository, procRepo repository.ProcedureRepository) sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		slog.Info("create_appointment_handler_started",
			"session_id", sess.ID,
			"phone", utils.MaskPhone(sess.PhoneNumber),
			"selected_slot_id", sess.GetContext("selected_slot_id"),
			"available_slots_len", len(sess.GetContext("available_slots_json")),
		)
		slot := findSelectedSlot(sess)
		if slot == nil {
			preview := sess.GetContext("available_slots_json")
			if len(preview) > 200 {
				preview = preview[:200] + "..."
			}
			slog.Warn("create_appointment_slot_not_found",
				"session_id", sess.ID,
				"selected_slot_id", sess.GetContext("selected_slot_id"),
				"available_slots_json_preview", preview,
			)
			return sm.NewResult(sm.StateBookingFailed).
				WithContext("booking_failure_reason", "slot_not_found"), nil
		}

		// Build observations
		isContrasted := sess.GetContext("is_contrasted") == "1"
		isSedated := sess.GetContext("is_sedated") == "1"
		observations := buildObservations(isContrasted, isSedated)

		entity := sess.GetContext("patient_entity")

		// Get current procedure group (already processed by grouper)
		proceduresJSON := sess.GetContext("procedures_json")
		var groups []services.CUPSGroup
		if err := json.Unmarshal([]byte(proceduresJSON), &groups); err != nil {
			// Defensive fallback: rebuild from individual context keys
			cupsCode := sess.GetContext("cups_code")
			cupsName := sess.GetContext("cups_name")
			espacios, _ := strconv.Atoi(sess.GetContext("espacios"))
			if espacios == 0 {
				espacios = 1
			}
			if cupsCode != "" {
				slog.Warn("procedures_json_recovered_from_context",
					"session_id", sess.ID,
					"phone", utils.MaskPhone(msg.Phone),
					"cups_code", cupsCode,
					"original_error", err,
				)
				groups = []services.CUPSGroup{{
					ServiceType: "general",
					Cups:        []services.CUPSEntry{{Code: cupsCode, Name: cupsName, Quantity: 1}},
					Espacios:    espacios,
				}}
				// Re-persist so advanceToNextProcedure can find it
				recovered, _ := json.Marshal(groups)
				sess.SetContext("procedures_json", string(recovered))
			} else {
				slog.Error("create_appointment_invalid_procedures_json",
					"session_id", sess.ID,
					"phone", utils.MaskPhone(msg.Phone),
					"error", err,
					"procedures_json_preview", truncate(proceduresJSON, 150),
				)
				return sm.NewResult(sm.StateBookingFailed).
					WithContext("booking_failure_reason", "error").
					WithEvent("appointment_create_error", map[string]interface{}{"error": "invalid procedures_json"}), nil
			}
		}

		currentIdx, _ := strconv.Atoi(sess.GetContext("current_procedure_idx"))
		if currentIdx >= len(groups) {
			return sm.NewResult(sm.StateBookingFailed).
				WithContext("booking_failure_reason", "error").
				WithEvent("appointment_create_error", map[string]interface{}{"error": "invalid procedure index"}), nil
		}

		currentGroup := groups[currentIdx]

		// Build map of original quantities from OCR (optional — absent in manual CUPS path)
		originalQuantities := make(map[string]int)
		if ocrJSON := sess.GetContext("ocr_cups_json"); ocrJSON != "" {
			var originalCups []services.CUPSEntry
			if err := json.Unmarshal([]byte(ocrJSON), &originalCups); err == nil {
				for _, cup := range originalCups {
					originalQuantities[cup.Code] = cup.Quantity
				}
				slog.Debug("OCR original CUPS before grouping", "original_cups", originalCups)
			}
		}

		// Build procedures list with ONLY CUPS from current group, but use original quantities
		var anyPricingFailed bool
		procedures := make([]domain.CreateProcedureInput, 0, len(currentGroup.Cups))
		for _, cupEntry := range currentGroup.Cups {
			// El Servicio del procedimiento ya NO se deriva aquí (era el ServiceID Antares, muerto):
			// el repo lo resuelve de forma canónica desde la tabla `servicios` (resolveProcServicio)
			// al insertar. Se deja en 0 (el repo lo ignora y recalcula).
			serviceID := 0

			// Get price from SOAT table based on the patient's CONTRACT manual.
			// GAP-3 (doc dudas §8): el manual debe salir del CONTRATO del paciente
			// (patient_contract, ya resuelto por régimen/municipio), NO de la entidad.
			// FindByCode con un código de contrato numérico devuelve el manual de ESE contrato;
			// con la entidad devolvía el del contrato principal (menor código) → cobraba tarifa
			// de Evento (manual 11) a pacientes MRC (contrato 5/6, manual 8). Fallback a la
			// entidad solo si no hay contrato en la sesión.
			priceLookupCode := entity
			if pc := sess.GetContext("patient_contract"); pc != "" {
				priceLookupCode = pc
			}
			var unitValue float64
			var pricingFailed bool
			if priceRepo != nil && entityRepo != nil {
				entityData, entityErr := entityRepo.FindByCode(ctx, priceLookupCode)
				if entityErr != nil {
					pricingFailed = true
					slog.Warn("entity_lookup_error_for_price",
						"entity_code", entity,
						"cup_code", cupEntry.Code,
						"error", entityErr,
					)
				} else if entityData == nil {
					pricingFailed = true
					slog.Warn("entity_not_found_for_price",
						"entity_code", entity,
						"cup_code", cupEntry.Code,
					)
				} else {
					// Normalize price type: "1" -> "01"
					priceType := entityData.PriceType
					if len(priceType) == 1 {
						priceType = "0" + priceType
					}
					price, priceErr := priceRepo.FindPrice(ctx, cupEntry.Code, priceType)
					if priceErr != nil {
						pricingFailed = true
						slog.Warn("price_lookup_error",
							"entity_code", entity,
							"price_type", priceType,
							"cup_code", cupEntry.Code,
							"error", priceErr,
						)
					} else if price == nil {
						pricingFailed = true
						slog.Warn("price_not_found",
							"entity_code", entity,
							"price_type", priceType,
							"cup_code", cupEntry.Code,
						)
					} else {
						unitValue = *price
					}
					slog.Debug("price_resolved",
						"entity_code", entity,
						"price_lookup_code", priceLookupCode,
						"price_type", priceType,
						"cup_code", cupEntry.Code,
						"unit_value", unitValue,
						"pricing_failed", pricingFailed,
					)
				}
			}
			// Si priceRepo/entityRepo no están inyectados (solo en tests) NO se bloquea: en
			// producción siempre están presentes y "sin convenio" se decide por el lookup real
			// (price == nil / error de tarifa) de arriba, no por la ausencia de los repos.
			if pricingFailed {
				anyPricingFailed = true
			}

			// Use quantity from original OCR if available, otherwise from grouped data
			quantity := cupEntry.Quantity
			if origQty, found := originalQuantities[cupEntry.Code]; found && origQty > 0 {
				quantity = origQty
				slog.Debug("Using original OCR quantity",
					"cup_code", cupEntry.Code,
					"grouped_quantity", cupEntry.Quantity,
					"original_quantity", origQty,
				)
			} else {
				slog.Debug("Using grouped quantity (not found in OCR)",
					"cup_code", cupEntry.Code,
					"grouped_quantity", cupEntry.Quantity,
				)
			}
			if quantity == 0 {
				quantity = 1
			}

			procedures = append(procedures, domain.CreateProcedureInput{
				CupCode:   cupEntry.Code,
				Quantity:  quantity,
				UnitValue: unitValue,
				ServiceID: serviceID,
			})
		}

		// N-22: precio null = sin convenio (igual que precio 0). El precio sale de (CUP, manual del
		// CONTRATO del paciente) — el mismo que valida el gate de cobertura y el que se guarda en
		// cpa.Valor. Si no se resolvió una tarifa válida para algún CUP, NO se agenda (evita
		// persistir Valor=0): se usa EXACTAMENTE el mismo flujo/mensaje que el gate de cobertura
		// para precio 0 (ofrecer particular o agente), no un mensaje aparte.
		if anyPricingFailed {
			slog.Warn("booking_blocked_no_price",
				"session_id", sess.ID,
				"cups_name", sess.GetContext("cups_name"),
				"entity", entity,
				"contract", sess.GetContext("patient_contract"),
			)
			cupsName := sess.GetContext("cups_name")
			if cupsName == "" {
				cupsName = sess.GetContext("cups_code")
			}
			return sm.NewResult(sm.StateCoverageNoConvenio).
				WithButtons(
					fmt.Sprintf("Tu EPS/contrato *no tiene convenio* para *%s*. Puedes agendarlo como *particular*, que tiene un costo.\n\n¿Cómo deseas continuar?", cupsName),
					sm.Button{Text: "Continuar particular", Payload: "cov_particular"},
					sm.Button{Text: "Hablar con agente", Payload: "cov_agente"},
				).
				WithEvent("coverage_no_convenio", map[string]interface{}{"cup": sess.GetContext("cups_code")}), nil
		}

		// Parse date
		date, _ := time.Parse("2006-01-02", slot.Date)

		slog.Debug("Creating appointment with CUPS from current group only",
			"procedures_count", len(procedures),
			"current_group_service", currentGroup.ServiceType,
			"current_group_cups", currentGroup.Cups,
		)

		doctorID := slot.DoctorSiesaCode
		if doctorID == "" {
			doctorID = slot.DoctorDoc // fallback: cédula si no hay código SIESA
		}

		// Resolve the SIESA subject (asunto) deterministically from the local CUPS catalog.
		// Sedation (patient-declared) overrides to asunto 17. This is the primary source for
		// citas.asunto; the repo only falls back to history if this is left at 0.
		subjectType := 0
		if procRepo != nil {
			for _, p := range procedures {
				a, aerr := procRepo.FindSubjectTypeForCups(ctx, p.CupCode)
				if aerr != nil {
					slog.Warn("find_subject_for_cups_failed", "cup_code", p.CupCode, "error", aerr)
					continue
				}
				if a > 0 {
					subjectType = a
					break
				}
			}
		}
		if sess.GetContext("is_sedated") == "1" {
			subjectType = 17 // SOPORTE SEDACION
		}
		slog.Debug("appointment_subject_resolved", "subject_type", subjectType, "is_sedated", sess.GetContext("is_sedated") == "1")

		input := domain.CreateAppointmentInput{
			Date:         date,
			TimeSlot:     slot.TimeSlot,
			DoctorID:     doctorID,
			PatientID:    sess.GetContext("patient_id"),
			Entity:       entity,
			AgendaID:     slot.AgendaID,
			AgendaSede:   slot.AgendaSede,
			CreatedBy:    "0", // Bot-created
			Observations: observations,
			SubjectType:  subjectType,
			ContractCode: sess.GetContext("patient_contract"),
			Procedures:   procedures,
		}

		// Pre-fetch old block BEFORE creating the new appointment.
		// FindConsecutiveBlock uses FindByAgendaAndDate, so if the patient rescheduled
		// to the same day/agenda/doctor the new appointment would appear in the results
		// and get included in the "old block", causing it to be cancelled alongside the original.
		rescheduleApptID := sess.GetContext("reschedule_appt_id")
		var oldBlockToCancel []domain.Appointment
		if rescheduleApptID != "" && sess.GetContext("reschedule_skip_cancel") != "1" {
			_, oldBlockToCancel, _ = apptSvc.FindBlockByAppointmentID(ctx, rescheduleApptID)
		}

		espacios, _ := strconv.Atoi(sess.GetContext("espacios"))
		apptID, err := apptSvc.CreateWithConsecutive(ctx, input, espacios)
		if err != nil {
			errMsg := err.Error()
			// Detect slot taken: explicit check OR MySQL duplicate/constraint violation.
			// "slots_consecutivos_insuficientes" (multi-slot: no caben N slots contiguos libres)
			// también es un fallo de disponibilidad → el paciente debe re-buscar, no auto-cerrar.
			if strings.Contains(errMsg, "slot_taken") || strings.Contains(errMsg, "Duplicate entry") ||
				strings.Contains(errMsg, "slots_consecutivos_insuficientes") {
				slog.Warn("create_appointment_slot_taken",
					"session_id", sess.ID,
					"phone", utils.MaskPhone(msg.Phone),
					"time_slot", slot.TimeSlot,
					"agenda_id", slot.AgendaID,
				)
				return sm.NewResult(sm.StateBookingFailed).
					WithContext("booking_failure_reason", "slot_taken"), nil
			}
			// Detect timeout — patient can retry
			reason := "error"
			if errors.Is(err, context.DeadlineExceeded) {
				reason = "timeout"
			}
			slog.Error("create_appointment_create_failed",
				"session_id", sess.ID,
				"phone", utils.MaskPhone(msg.Phone),
				"patient_name", sess.GetContext("patient_name"),
				"time_slot", slot.TimeSlot,
				"agenda_id", slot.AgendaID,
				"error", err,
			)
			return sm.NewResult(sm.StateBookingFailed).
				WithContext("booking_failure_reason", reason).
				WithEvent("appointment_create_error", map[string]interface{}{"error": errMsg}), nil
		}
		slog.Info("create_appointment_success",
			"session_id", sess.ID,
			"appointment_id", apptID,
			"time_slot", slot.TimeSlot,
		)

		// Cancel old appointment if this is a self-service reschedule (block pre-fetched above)
		if rescheduleApptID != "" && sess.GetContext("reschedule_skip_cancel") != "1" {
			if len(oldBlockToCancel) > 0 {
				if cancelErr := apptSvc.CancelBlock(ctx, oldBlockToCancel, "reprogramada por paciente via bot", "whatsapp_bot", ""); cancelErr != nil {
					slog.Error("reschedule: cancel old block", "error", cancelErr, "phone", utils.MaskPhone(msg.Phone), "old_appt_id", rescheduleApptID)
				} else {
					slog.Info("reschedule: old appointment cancelled",
						"old_appt_id", rescheduleApptID,
						"new_appt_id", apptID,
						"block_size", len(oldBlockToCancel))
				}
			} else {
				slog.Warn("reschedule: old block not found", "old_appt_id", rescheduleApptID)
			}
		}

		result := sm.NewResult(sm.StateBookingSuccess).
			WithContext("created_appointment_id", apptID).
			WithEvent("appointment_created", map[string]interface{}{
				"appointment_id":  apptID,
				"cups_codes":      len(procedures),
				"cups_code":       sess.GetContext("cups_code"),
				"cups_name":       sess.GetContext("cups_name"),
				"service_type":    currentGroup.ServiceType,
				"date":            slot.Date,
				"time":            slot.TimeDisplay,
				"doctor":          slot.DoctorName,
				"espacios":        espacios,
				"reschedule_from": rescheduleApptID,
			})

		if wlID := sess.GetContext("waiting_list_entry_id"); wlID != "" {
			result.WithEvent("waiting_list_booking_success", map[string]interface{}{
				"waiting_list_id": wlID,
				"appointment_id":  apptID,
			})
		}

		return result, nil
	}
}

// BOOKING_SUCCESS (automático) — cita creada exitosamente.
func bookingSuccessHandler(addrMapper *services.AddressMapper) sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		slot := findSelectedSlot(sess)
		cupsName := sess.GetContext("cups_name")

		var doctorName, dateDisplay, timeDisplay, address string
		if slot != nil {
			doctorName = slot.DoctorName
			timeDisplay = slot.TimeDisplay
			address = slot.ClinicAddress
			if dt, err := time.Parse("2006-01-02", slot.Date); err == nil {
				dateDisplay = utils.FormatFriendlyDate(dt)
			} else {
				dateDisplay = slot.Date
			}
		}

		header := "*Cita agendada exitosamente*"
		if sess.GetContext("reschedule_appt_id") != "" {
			header = "*Tu cita ha sido reprogramada exitosamente*"
		}

		successMsg := fmt.Sprintf("%s\n\n"+
			"Procedimiento: %s\n"+
			"Doctor: Dr. %s\n"+
			"Fecha: %s\n"+
			"Hora: %s",
			header, cupsName, doctorName, dateDisplay, timeDisplay)

		if address != "" {
			if addrMapper != nil {
				successMsg += "\n" + addrMapper.FormatAddress(address)
			} else {
				successMsg += fmt.Sprintf("\nDirección: %s", address)
			}
		}

		// Preparation instructions
		if prep := sess.GetContext("cups_preparation"); prep != "" {
			successMsg += fmt.Sprintf("\n\n📋 *Preparación:*\n%s", prep)
		}
		if videoURL := sess.GetContext("cups_video_url"); videoURL != "" {
			successMsg += fmt.Sprintf("\n\n🎥 *Video de preparación:*\n%s", videoURL)
		}
		if audioURL := sess.GetContext("cups_audio_url"); audioURL != "" {
			successMsg += fmt.Sprintf("\n\n🎵 *Audio:*\n%s", audioURL)
		}

		successMsg += "\n\nRecuerda presentarte 30 minutos antes para realizar el proceso de facturación, con tu documento y orden médica."

		// Check multi-procedure flow
		if next := advanceToNextProcedure(sess); next != nil {
			next.Messages = append([]sm.OutboundMessage{&sm.TextMessage{Text: successMsg}}, next.Messages...)
			return next.WithEvent("booking_success", map[string]interface{}{
				"appointment_id": sess.GetContext("created_appointment_id"),
				"cups_code":      sess.GetContext("cups_code"),
				"next_procedure": true,
			}), nil
		}

		// No more procedures → auto-close
		return buildAutoCloseResult(successMsg).
			WithEvent("booking_success", map[string]interface{}{
				"appointment_id": sess.GetContext("created_appointment_id"),
				"cups_code":      sess.GetContext("cups_code"),
				"next_procedure": false,
			}), nil
	}
}

// BOOKING_FAILED (automático) — error al crear cita.
func bookingFailedHandler() sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		reason := sess.GetContext("booking_failure_reason")

		switch reason {
		case "slot_taken":
			return sm.NewResult(sm.StateSearchSlots).
				WithText("El horario que seleccionaste ya fue tomado por otro paciente. Buscando nuevos horarios...").
				WithClearCtx("selected_slot_id", "available_slots_json").
				WithEvent("slot_taken", nil), nil

		case "timeout":
			return sm.NewResult(sm.StateSearchSlots).
				WithText("Tardó demasiado crear la cita. Buscando horarios nuevamente...").
				WithClearCtx("selected_slot_id", "available_slots_json").
				WithEvent("booking_timeout", nil), nil

		default:
			return buildAutoCloseResult("Ocurrió un error al crear la cita. Por favor intenta más tarde.").
				WithEvent("booking_failed", map[string]interface{}{"reason": reason}), nil
		}
	}
}

// --- Helpers ---

// findSelectedSlot retrieves the selected slot from session context.
func findSelectedSlot(sess *session.Session) *services.AvailableSlot {
	selectedSlotID := sess.GetContext("selected_slot_id")
	var slots []services.AvailableSlot
	json.Unmarshal([]byte(sess.GetContext("available_slots_json")), &slots)

	for _, s := range slots {
		if s.TimeSlot == selectedSlotID {
			return &s
		}
	}
	return nil
}

// buildBookingSummary creates the booking confirmation text.
func buildBookingSummary(sess *session.Session, slot *services.AvailableSlot, addrMapper *services.AddressMapper) string {
	dateDisplay := slot.Date
	if dt, err := time.Parse("2006-01-02", slot.Date); err == nil {
		dateDisplay = utils.FormatFriendlyDate(dt)
	}

	summary := fmt.Sprintf("*Resumen de tu cita:*\n\n"+
		"Procedimiento: %s\n"+
		"Doctor: Dr. %s\n"+
		"Fecha: %s\n"+
		"Hora: %s",
		sess.GetContext("cups_name"),
		slot.DoctorName,
		dateDisplay,
		slot.TimeDisplay)

	if slot.ClinicAddress != "" {
		if addrMapper != nil {
			summary += "\n" + addrMapper.FormatAddress(slot.ClinicAddress)
		} else {
			summary += fmt.Sprintf("\nDirección: %s", slot.ClinicAddress)
		}
	}

	if prep := sess.GetContext("cups_preparation"); prep != "" {
		summary += fmt.Sprintf("\n\n📋 *Preparación:*\n%s", prep)
	}
	if videoURL := sess.GetContext("cups_video_url"); videoURL != "" {
		summary += fmt.Sprintf("\n\n🎥 *Video:* %s", videoURL)
	}
	if audioURL := sess.GetContext("cups_audio_url"); audioURL != "" {
		summary += fmt.Sprintf("\n\n🎵 *Audio:* %s", audioURL)
	}

	summary += "\n\n¿Confirmas esta cita?"

	return summary
}

// buildObservations creates the observations string for the appointment.
func buildObservations(isContrasted, isSedated bool) string {
	var parts []string
	if isContrasted {
		parts = append(parts, "Contrastada")
	}
	if isSedated {
		parts = append(parts, "Bajo Sedación")
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, ", ")
}
