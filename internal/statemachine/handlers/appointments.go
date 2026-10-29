package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/observability"
	"github.com/neuro-bot/neuro-bot/internal/repository"
	"github.com/neuro-bot/neuro-bot/internal/services"
	"github.com/neuro-bot/neuro-bot/internal/session"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
	"github.com/neuro-bot/neuro-bot/internal/utils"
)

// CancellationCallback is called after a patient cancels an appointment, once per unique freed slot
// identified by (codMedi, agendaID). A freed slot belongs to a doctor+agenda (not a CUP): it can be
// reused by any CUPS the doctor performs within the agenda's subjects, so matching keys on the slot.
type CancellationCallback func(ctx context.Context, codMedi, agendaID int)

// notifyFreedSlots avisa a la lista de espera por cada (médico, agenda) único liberado en el bloque
// cancelado. Debe llamarse con las citas ANTES de cancelarlas (para tener cod_medi/agenda). Es
// fire-and-forget: usa context.WithoutCancel para no abortar si el ctx del handler se cancela (N-32).
func notifyFreedSlots(ctx context.Context, onCancel CancellationCallback, block []domain.Appointment) {
	if onCancel == nil {
		return
	}
	seen := make(map[string]bool)
	for _, appt := range block {
		cod, _ := strconv.Atoi(appt.DoctorID)
		if cod <= 0 || appt.AgendaID <= 0 {
			continue
		}
		key := strconv.Itoa(cod) + "-" + strconv.Itoa(appt.AgendaID)
		if seen[key] {
			continue
		}
		seen[key] = true
		go onCancel(context.WithoutCancel(ctx), cod, appt.AgendaID)
	}
}

// RegisterAppointmentHandlers registra los handlers del flujo de consulta de citas.
func RegisterAppointmentHandlers(m *sm.Machine, apptSvc *services.AppointmentService, procRepo repository.ProcedureRepository, addrMapper *services.AddressMapper, onCancel CancellationCallback) {
	m.Register(sm.StateFetchAppointments, fetchAppointmentsHandler(apptSvc, procRepo))
	m.Register(sm.StateListAppointments, listAppointmentsHandler(apptSvc, procRepo))
	sm.RegisterReturnMenu(sm.StateListAppointments, appointmentReturnOptions)
	m.Register(sm.StateAppointmentAction, appointmentActionHandler(apptSvc, procRepo, addrMapper))
	sm.RegisterStaticReturnMenu(sm.StateAppointmentAction,
		payloadOption("Confirmar la cita", "appt_confirm"),
		payloadOption("Cancelar la cita", "appt_cancel"),
		payloadOption("Reagendar la cita", "appt_reschedule"),
		payloadOption("Ver la preparación", "appt_preparation"),
		payloadOption("Volver a la lista de citas", "appt_back"),
		payloadOption("Ir al menú principal", "appt_menu"),
	)
	m.Register(sm.StateConfirmAppointment, confirmAppointmentHandler(apptSvc, procRepo, addrMapper))
	sm.RegisterStaticReturnMenu(sm.StateConfirmAppointment,
		payloadOption("Sí, confirmar la cita", "confirm_yes"),
		payloadOption("No, cancelar la acción", "confirm_no"),
	)
	m.Register(sm.StateCancelAppointment, cancelAppointmentHandler(apptSvc, procRepo, onCancel))
	sm.RegisterStaticReturnMenu(sm.StateCancelAppointment,
		payloadOption("Sí, cancelar la cita", "cancel_yes"),
		payloadOption("No, mantener la cita", "cancel_no"),
	)
	m.Register(sm.StateNoAppointments, noAppointmentsHandler())
	sm.RegisterStaticReturnMenu(sm.StateNoAppointments,
		payloadOption("Volver al menú principal", "no_appt_menu"),
		payloadOption("Terminar el chat", "no_appt_end"),
	)

	// Flujos de confirmación desde notificaciones proactivas
	confirmReschedulePrompt := func(sess *session.Session, result *sm.StateResult) {
		result.WithButtons(
			fmt.Sprintf(
				"¿Confirmas que deseas reprogramar tu cita?\n\n"+
					"📅 *Fecha actual:* %s\n"+
					"🕐 *Hora:* %s\n"+
					"💊 *Procedimiento:* %s",
				sess.GetContext("notif_appt_date"),
				sess.GetContext("notif_appt_time"),
				sess.GetContext("notif_cups_name"),
			),
			sm.Button{Text: "Sí, reprogramar", Payload: "reschedule_yes"},
			sm.Button{Text: "No, mantener", Payload: "reschedule_no"},
		)
	}
	sm.RegisterStaticReturnMenu(sm.StateConfirmRescheduleNotif,
		payloadOption("Sí, buscar nuevos horarios", "reschedule_yes"),
		payloadOption("No, mantener la cita", "reschedule_no"),
	)
	m.RegisterWithConfig(sm.StateConfirmRescheduleNotif, sm.HandlerConfig{
		InputType:   sm.InputButton,
		Options:     []string{"reschedule_yes", "reschedule_no"},
		ErrorMsg:    "Por favor selecciona una de las opciones.",
		RetryPrompt: confirmReschedulePrompt,
		Handler:     confirmRescheduleNotifHandler(apptSvc, procRepo),
	})

	confirmCancelPrompt := func(sess *session.Session, result *sm.StateResult) {
		result.WithButtons(
			fmt.Sprintf(
				"¿Confirmas que deseas cancelar tu cita?\n\n"+
					"📅 *Fecha:* %s\n"+
					"🕐 *Hora:* %s\n"+
					"💊 *Procedimiento:* %s",
				sess.GetContext("notif_appt_date"),
				sess.GetContext("notif_appt_time"),
				sess.GetContext("notif_cups_name"),
			),
			sm.Button{Text: "Sí, cancelar", Payload: "cancel_yes"},
			sm.Button{Text: "No, mantener", Payload: "cancel_no"},
		)
	}
	sm.RegisterStaticReturnMenu(sm.StateConfirmCancelNotif,
		payloadOption("Sí, cancelar la cita", "cancel_yes"),
		payloadOption("No, mantener la cita", "cancel_no"),
	)
	m.RegisterWithConfig(sm.StateConfirmCancelNotif, sm.HandlerConfig{
		InputType:   sm.InputButton,
		Options:     []string{"cancel_yes", "cancel_no"},
		ErrorMsg:    "Por favor selecciona una de las opciones.",
		RetryPrompt: confirmCancelPrompt,
		Handler:     confirmCancelNotifHandler(apptSvc, onCancel),
	})

	// NOTIF_PENDING: menú principal de notificación (3 botones)
	notifPendingPrompt := func(sess *session.Session, result *sm.StateResult) {
		result.WithButtons(
			fmt.Sprintf(
				"📅 *Fecha:* %s\n"+
					"🕐 *Hora:* %s\n"+
					"💊 *Procedimiento:* %s\n\n"+
					"¿Qué deseas hacer con tu cita?",
				sess.GetContext("notif_appt_date"),
				sess.GetContext("notif_appt_time"),
				sess.GetContext("notif_cups_name"),
			),
			sm.Button{Text: "Confirmar cita", Payload: "confirm"},
			sm.Button{Text: "Reprogramar", Payload: "reschedule"},
			sm.Button{Text: "Cancelar cita", Payload: "cancel"},
		)
	}
	// El agente resuelve la notificación por el paciente (`/bot resume NOTIF_PENDING <acción>`).
	// Sin campo a propósito: el NotificationManager arma el descriptor de este paso sin máquina,
	// con la vista de la sesión. El worker reconoce State == NOTIF_PENDING con Data = acción y la
	// ejecuta sin reanudar el flujo.
	sm.RegisterReturnMenu(sm.StateNotifPending, notifPendingReturnOptions)
	// «Devolver al bot en el mismo paso» re-pide la confirmación con los 3 botones: solo se
	// ofrece donde esos botones corresponden a la notificación.
	sm.HidePlainResumeWhen(sm.StateNotifPending, notifPendingButtonsDontApply)
	// La notificación deja una cita (o una acción de cita) por resolver.
	sm.RegisterResolveGuard(sm.StateNotifPending, sm.ResolveGuardPendingAppointment)
	m.RegisterWithConfig(sm.StateNotifPending, sm.HandlerConfig{
		InputType:   sm.InputButton,
		Options:     []string{"confirm", "reschedule", "cancel"},
		ErrorMsg:    "Por favor selecciona una opción.",
		RetryPrompt: notifPendingPrompt,
		Handler:     notifPendingHandler(apptSvc, procRepo, addrMapper),
	})

	// Fallback: reschedule sin slots → ofrece Confirmar/Cancelar
	sm.RegisterStaticReturnMenu(sm.StateNotifRescheduleFallback,
		payloadOption("Confirmar la cita", "confirm"),
		payloadOption("Cancelar la cita", "cancel"),
	)
	m.RegisterWithConfig(sm.StateNotifRescheduleFallback, sm.HandlerConfig{
		InputType: sm.InputButton,
		Options:   []string{"confirm", "cancel"},
		ErrorMsg:  "Por favor selecciona una opción.",
		RetryPrompt: resumeOnlyRetryPrompt(func(sess *session.Session) sm.OutboundMessage {
			return &sm.ButtonMessage{
				Text: rescheduleFallbackQuestion(sess.GetContext("cups_name")),
				Buttons: []sm.Button{
					{Text: "Confirmar cita", Payload: "confirm"},
					{Text: "Cancelar cita", Payload: "cancel"},
				},
			}
		}),
		Handler: notifRescheduleFallbackHandler(apptSvc, procRepo, addrMapper, onCancel),
	})
}

// FETCH_APPOINTMENTS (automático) — consulta citas del paciente y muestra la lista
func fetchAppointmentsHandler(apptSvc *services.AppointmentService, procRepo repository.ProcedureRepository) sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		patientID := sess.GetContext("patient_id")

		appointments, err := apptSvc.GetUpcomingAppointments(ctx, patientID)
		if err != nil {
			msg := "No pudimos consultar tus citas en este momento. Intenta más tarde."
			eventType := "fetch_appointments_error"
			if errors.Is(err, context.DeadlineExceeded) {
				msg = "Tardó demasiado consultar tus citas. Por favor intenta de nuevo."
				eventType = "fetch_appointments_timeout"
			}
			return buildAutoCloseResult(msg).
				WithEvent(eventType, map[string]interface{}{"error": err.Error()}), nil
		}

		if len(appointments) == 0 {
			observability.Emit(observability.TraceSession(sess.ID), "mis_citas", "no_appointments",
				observability.EmitOpts{Phone: sess.PhoneNumber})
			return buildAutoCloseResult("No tienes citas pendientes o confirmadas.").
				WithEvent("no_appointments_found", nil), nil
		}

		// Serializar citas en contexto para los siguientes estados
		apptJSON, err := json.Marshal(appointments)
		if err != nil {
			slog.Error("appointments_marshal_failed", "error", err)
			return buildAutoCloseResult("Error interno al procesar tus citas. Por favor intenta de nuevo.").
				WithEvent("appointments_marshal_error", map[string]interface{}{"error": err.Error()}), nil
		}

		// Generar la lista aquí (LIST_APPOINTMENTS es interactivo, no auto-chain)
		listMsg := buildAppointmentList(ctx, apptSvc, appointments, procRepo)

		observability.Emit(observability.TraceSession(sess.ID), "mis_citas", "appointments_listed",
			observability.EmitOpts{Phone: sess.PhoneNumber, Attrs: map[string]interface{}{"count": len(appointments)}})
		return sm.NewResult(sm.StateListAppointments).
			WithContext("appointments_json", string(apptJSON)).
			WithList(listMsg.body, listMsg.button, listMsg.section).
			WithEvent("appointments_found", map[string]interface{}{"count": len(appointments)}), nil
	}
}

// LIST_APPOINTMENTS (interactivo, lista) — espera selección de cita, muestra detalle al seleccionar
func listAppointmentsHandler(apptSvc *services.AppointmentService, procRepo repository.ProcedureRepository) sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		// Si es postback con ID de cita seleccionada
		if msg.IsPostback {
			var appts []domain.Appointment
			if err := json.Unmarshal([]byte(sess.GetContext("appointments_json")), &appts); err != nil {
				slog.Error("appointments_unmarshal_failed", "error", err, "location", "listAppointments_postback")
				return buildAutoCloseResult("No pudimos cargar tus citas. Por favor intenta de nuevo.").
					WithEvent("appointments_unmarshal_error", map[string]interface{}{"error": err.Error()}), nil
			}
			for _, a := range appts {
				if a.ID == msg.PostbackPayload {
					sess.RetryCount = 0

					// Mostrar detalle + lista de acciones en un solo mensaje
					detail := buildAppointmentDetail(ctx, apptSvc, appts, a.ID, procRepo)
					return sm.NewResult(sm.StateAppointmentAction).
						WithContext("selected_appointment_id", msg.PostbackPayload).
						WithList(
							detail+"\n\n¿Qué deseas hacer con esta cita?", "Ver opciones",
							sm.ListSection{Title: "Acciones", Rows: appointmentActionRows()},
						).
						WithEvent("appointment_selected", map[string]interface{}{"id": msg.PostbackPayload}), nil
				}
			}
			// Invalid postback ID — fall through to retry + re-show list
		}

		// Selección numérica por agente: /bot resume LIST_APPOINTMENTS 1
		if n, err := strconv.Atoi(strings.TrimSpace(msg.Text)); err == nil && n >= 1 {
			var appts []domain.Appointment
			if jsonErr := json.Unmarshal([]byte(sess.GetContext("appointments_json")), &appts); jsonErr != nil {
				slog.Error("appointments_unmarshal_failed", "error", jsonErr, "location", "listAppointments_numeric")
				return buildAutoCloseResult("No pudimos cargar tus citas. Por favor intenta de nuevo.").
					WithEvent("appointments_unmarshal_error", map[string]interface{}{"error": jsonErr.Error()}), nil
			}
			if n <= len(appts) {
				selected := appts[n-1]
				sess.RetryCount = 0
				detail := buildAppointmentDetail(ctx, apptSvc, appts, selected.ID, procRepo)
				return sm.NewResult(sm.StateAppointmentAction).
					WithContext("selected_appointment_id", selected.ID).
					WithList(
						detail+"\n\n¿Qué deseas hacer con esta cita?", "Ver opciones",
						sm.ListSection{Title: "Acciones", Rows: appointmentActionRows()},
					).
					WithEvent("appointment_selected", map[string]interface{}{"id": selected.ID}), nil
			}
		}

		// Texto o postback inválido — retry antes de re-mostrar lista
		result := sm.RetryOrEscalate(sess, "Selecciona una cita de la lista.")
		if result.NextState == sm.StateEscalateToAgent {
			return result, nil
		}

		// Cargar citas del contexto y re-mostrar lista
		var appointments []domain.Appointment
		if err := json.Unmarshal([]byte(sess.GetContext("appointments_json")), &appointments); err != nil {
			return buildAutoCloseResult("No pudimos cargar tus citas. Por favor intenta de nuevo."), nil
		}

		listMsg := buildAppointmentList(ctx, apptSvc, appointments, procRepo)

		return sm.NewResult(sess.CurrentState).
			WithList(listMsg.body, listMsg.button, listMsg.section).
			WithEvent("appointments_listed", map[string]interface{}{"shown": len(listMsg.section.Rows)}), nil
	}
}

// APPOINTMENT_ACTION (interactivo, lista) — procesa acción seleccionada sobre la cita
func appointmentActionHandler(apptSvc *services.AppointmentService, procRepo repository.ProcedureRepository, addrMapper *services.AddressMapper) sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		selectedID := sess.GetContext("selected_appointment_id")

		result, selected := sm.ValidateButtonResponse(sess, msg, "appt_confirm", "appt_cancel", "appt_reschedule", "appt_preparation", "appt_back", "appt_menu")
		if result != nil {
			if result.NextState == sm.StateEscalateToAgent {
				return result, nil
			}

			// Re-mostrar detalle + acciones
			var appointments []domain.Appointment
			if err := json.Unmarshal([]byte(sess.GetContext("appointments_json")), &appointments); err != nil {
				return buildAutoCloseResult("No pudimos cargar tus citas en este momento."), nil
			}

			detail := buildAppointmentDetail(ctx, apptSvc, appointments, selectedID, procRepo)
			if detail == "" {
				return sm.NewResult(sm.StateListAppointments).
					WithText("Cita no encontrada. Selecciona otra.").
					WithClearCtx("selected_appointment_id"), nil
			}

			return sm.NewResult(sess.CurrentState).
				WithList(
					detail+"\n\n¿Qué deseas hacer con esta cita?", "Ver opciones",
					sm.ListSection{Title: "Acciones", Rows: appointmentActionRows()},
				), nil
		}

		switch selected {
		case "appt_confirm":
			return sm.NewResult(sm.StateConfirmAppointment).
				WithButtons(
					"¿Estás seguro de *confirmar* esta cita?",
					sm.Button{Text: "Sí, confirmar", Payload: "confirm_yes"},
					sm.Button{Text: "No, volver", Payload: "confirm_no"},
				).
				WithEvent("appointment_confirm_requested", nil), nil

		case "appt_cancel":
			return sm.NewResult(sm.StateCancelAppointment).
				WithButtons(
					"¿Estás seguro de *cancelar* esta cita? Esta acción no se puede deshacer.",
					sm.Button{Text: "Sí, cancelar", Payload: "cancel_yes"},
					sm.Button{Text: "No, volver", Payload: "cancel_no"},
				).
				WithEvent("appointment_cancel_requested", nil), nil

		case "appt_reschedule":
			observability.Emit(observability.TraceSession(sess.ID), "mis_citas", "reschedule_started",
				observability.EmitOpts{Phone: sess.PhoneNumber})
			// Extraer datos de la cita existente y buscar slots directamente.
			// La cita vieja NO se cancela — solo se cancela cuando se crea la nueva
			// (via reschedule_appt_id en createAppointmentHandler).
			var appointments []domain.Appointment
			if err := json.Unmarshal([]byte(sess.GetContext("appointments_json")), &appointments); err != nil {
				slog.Error("appointments_unmarshal_failed", "error", err, "location", "appointmentAction_reschedule")
				return buildAutoCloseResult("No pudimos cargar tus citas para reprogramar. Por favor intenta de nuevo.").
					WithEvent("appointments_unmarshal_error", map[string]interface{}{"error": err.Error()}), nil
			}

			var selectedAppt *domain.Appointment
			for i, a := range appointments {
				if a.ID == selectedID {
					selectedAppt = &appointments[i]
					break
				}
			}
			if selectedAppt == nil {
				return sm.NewResult(sm.StateListAppointments).
					WithText("Cita no encontrada."), nil
			}

			cupsCode, cupsName := "", ""
			if len(selectedAppt.Procedures) > 0 {
				cupsCode = selectedAppt.Procedures[0].CupCode
				cupsName = selectedAppt.Procedures[0].CupName
			}
			if cupsCode == "" {
				return buildAutoCloseResult("No se puede reprogramar: no se encontró el procedimiento."), nil
			}

			// Resolver nombre cuando SIESA solo almacena el código (puede ser "891509-16" → buscar "891509")
			if (cupsName == "" || cupsName == cupsCode) && procRepo != nil {
				if p, lookupErr := procRepo.FindByCode(ctx, utils.BaseCupCode(cupsCode)); lookupErr == nil && p != nil && p.Name != "" {
					cupsName = p.Name
				}
			}

			// Contraste al reprogramar: observación de SIESA + nombre del/los CUPS (una cita creada por
			// un agente puede no traer "Contrastada" en la observación pero sí "CON CONTRASTE" en el
			// nombre). Sin esto se reprogramaría como simple y podría ofrecer un slot fuera de ventana.
			procNames := []string{cupsName}
			for _, p := range selectedAppt.Procedures {
				procNames = append(procNames, p.CupName)
			}
			isContrasted := "0"
			if utils.AppointmentIsContrasted(selectedAppt.Observations, procNames...) {
				isContrasted = "1"
			}
			// Sedación: observación + asunto de la cita (17 = agenda de sedación). Los agentes casi
			// nunca escriben "sedación" en la observación, pero sí crean la cita en el asunto 17; sin
			// mirar el asunto, se reprogramaría como normal perdiendo el ruteo a la agenda de sedación.
			asunto, _ := apptSvc.AppointmentAsunto(ctx, selectedAppt.ID)
			isSedated := "0"
			if utils.AppointmentIsSedated(selectedAppt.Observations, asunto) {
				isSedated = "1"
			}

			// Modelo multi-slot: una cita ocupa N slots (programacion_medico_detalle.IdCita).
			// El conteo real de slots de la cita es la fuente de verdad de cuántos espacios
			// re-reservar; el override por SlotCountForAppointment/SpacesForCUPS de abajo manda.
			espacios := 1
			if n, scErr := apptSvc.SlotCountForAppointment(ctx, selectedID); scErr == nil && n > espacios {
				espacios = n
			}
			if espacios == 0 {
				espacios = 1
			}
			// Para resonancias: recalcular espacios según código y contraste (determinístico por CUPS)
			if calculated, ok := services.SpacesForCUPS(cupsCode, isContrasted == "1"); ok {
				espacios = calculated
			}

			// Build procedures_json required by createAppointmentHandler
			cups := make([]services.CUPSEntry, 0, len(selectedAppt.Procedures))
			for _, p := range selectedAppt.Procedures {
				name := p.CupName
				if (name == "" || name == p.CupCode) && procRepo != nil {
					if proc, lookupErr := procRepo.FindByCode(ctx, utils.BaseCupCode(p.CupCode)); lookupErr == nil && proc != nil && proc.Name != "" {
						name = proc.Name
					}
				}
				cups = append(cups, services.CUPSEntry{
					Code: p.CupCode, Name: name, Quantity: 1,
					IsContrasted: isContrasted == "1",
				})
			}
			groups := []services.CUPSGroup{{
				ServiceType: "general",
				Cups:        cups,
				Espacios:    espacios,
			}}
			proceduresJSON, _ := json.Marshal(groups)

			// N-23: preservar la edad real del paciente (la sesión ya la tiene). Antes se ponía
			// "0", lo que descartaba slots de médicos con restricción de edad al reprogramar.
			patientAge := sess.GetContext("patient_age")
			if patientAge == "" {
				patientAge = "0"
			}

			return sm.NewResult(sm.StateSearchSlots).
				WithContext("cups_code", cupsCode).
				WithContext("cups_name", cupsName).
				WithContext("is_contrasted", isContrasted).
				WithContext("is_sedated", isSedated).
				WithContext("espacios", fmt.Sprintf("%d", espacios)).
				WithContext("preferred_doctor_doc", selectedAppt.DoctorDocument). // N14: cédula, no cod_medi
				WithContext("total_procedures", "1").
				WithContext("current_procedure_idx", "0").
				WithContext("reschedule_appt_id", selectedAppt.ID).
				WithContext("patient_age", patientAge).
				WithContext("procedures_json", string(proceduresJSON)).
				WithText("Buscando horarios disponibles para reprogramar tu cita de *"+cupsName+"*...").
				WithEvent("appointment_reschedule_started", map[string]interface{}{
					"old_appt_id": selectedAppt.ID,
					"cups_code":   cupsCode,
				}), nil

		case "appt_preparation":
			return showAppointmentPreparation(ctx, sess, apptSvc, procRepo, addrMapper)

		case "appt_back":
			var appointments []domain.Appointment
			if err := json.Unmarshal([]byte(sess.GetContext("appointments_json")), &appointments); err != nil {
				slog.Error("appointments_unmarshal_failed", "error", err, "location", "appointmentAction_back")
				return buildAutoCloseResult("No pudimos cargar tus citas. Por favor intenta de nuevo.").
					WithEvent("appointments_unmarshal_error", map[string]interface{}{"error": err.Error()}), nil
			}
			listMsg := buildAppointmentList(ctx, apptSvc, appointments, procRepo)

			return sm.NewResult(sm.StateListAppointments).
				WithList(listMsg.body, listMsg.button, listMsg.section).
				WithClearCtx("selected_appointment_id"), nil

		case "appt_menu":
			r := sm.NewResult(sm.StateMainMenu).
				WithClearCtx("selected_appointment_id", "appointments_json")
			r.Messages = append(r.Messages, buildMainMenuList())
			return r.WithEvent("appointment_back_to_menu", nil), nil
		}

		return nil, fmt.Errorf("unreachable: selected=%s", selected)
	}
}

// CONFIRM_APPOINTMENT (interactivo) — reconfirmación antes de confirmar la cita
func confirmAppointmentHandler(apptSvc *services.AppointmentService, procRepo repository.ProcedureRepository, addrMapper *services.AddressMapper) sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		result, selected := sm.ValidateButtonResponse(sess, msg, "confirm_yes", "confirm_no")
		if result != nil {
			if result.NextState == sm.StateEscalateToAgent {
				return result, nil
			}
			result.Messages = nil
			return sm.NewResult(sess.CurrentState).
				WithButtons(
					"¿Estás seguro de *confirmar* esta cita?",
					sm.Button{Text: "Sí, confirmar", Payload: "confirm_yes"},
					sm.Button{Text: "No, volver", Payload: "confirm_no"},
				), nil
		}

		switch selected {
		case "confirm_yes":
			return executeConfirmAppointment(ctx, sess, apptSvc, procRepo, addrMapper)

		case "confirm_no":
			// Volver al detalle de la cita
			return backToAppointmentAction(ctx, sess, apptSvc, procRepo), nil
		}

		return nil, fmt.Errorf("unreachable: selected=%s", selected)
	}
}

// CANCEL_APPOINTMENT (interactivo) — reconfirmación antes de cancelar la cita.
func cancelAppointmentHandler(apptSvc *services.AppointmentService, procRepo repository.ProcedureRepository, onCancel CancellationCallback) sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		result, selected := sm.ValidateButtonResponse(sess, msg, "cancel_yes", "cancel_no")
		if result != nil {
			if result.NextState == sm.StateEscalateToAgent {
				return result, nil
			}
			result.Messages = nil
			return sm.NewResult(sess.CurrentState).
				WithButtons(
					"¿Estás seguro de *cancelar* esta cita? Esta acción no se puede deshacer.",
					sm.Button{Text: "Sí, cancelar", Payload: "cancel_yes"},
					sm.Button{Text: "No, volver", Payload: "cancel_no"},
				), nil
		}

		switch selected {
		case "cancel_yes":
			return executeCancelAppointment(ctx, sess, apptSvc, onCancel)

		case "cancel_no":
			return backToAppointmentAction(ctx, sess, apptSvc, procRepo), nil
		}

		return nil, fmt.Errorf("unreachable: selected=%s", selected)
	}
}

// NO_APPOINTMENTS (interactivo) — menú cuando no hay citas
func noAppointmentsHandler() sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		result, selected := sm.ValidateButtonResponse(sess, msg, "no_appt_menu", "no_appt_end")
		if result != nil {
			if result.NextState == sm.StateEscalateToAgent {
				return result, nil
			}
			result.Messages = nil
			return sm.NewResult(sess.CurrentState).
				WithButtons(
					"No tienes citas pendientes o confirmadas.\n\n¿Qué deseas hacer?",
					sm.Button{Text: "Menú principal", Payload: "no_appt_menu"},
					sm.Button{Text: "Terminar chat", Payload: "no_appt_end"},
				), nil
		}

		switch selected {
		case "no_appt_menu":
			r := sm.NewResult(sm.StateMainMenu)
			r.Messages = append(r.Messages, buildMainMenuList())
			return r.WithEvent("no_appt_back_to_menu", nil), nil

		case "no_appt_end":
			return sm.NewResult(sm.StateFarewell).
				WithEvent("no_appt_farewell", nil), nil
		}

		return nil, fmt.Errorf("unreachable: selected=%s", selected)
	}
}

// confirmRescheduleNotifHandler handles CONFIRM_RESCHEDULE_NOTIF state.
// Patient pressed "Reprogramar" on the proactive confirmation template.
// We ask them to confirm (1/2) before launching the slot search flow.
func confirmRescheduleNotifHandler(apptSvc *services.AppointmentService, procRepo repository.ProcedureRepository) sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		selected := sm.ValidatedPayload(ctx)
		switch selected {
		case "reschedule_yes":
			// Si el paciente tiene varias citas ese día (notificadas juntas), se le deja elegir
			// CUÁL reprogramar (no se pueden reagendar todas a un mismo horario).
			apptID := sess.GetContext("reschedule_appt_id")
			if apptID == "" {
				apptID = notifAppointmentID(sess)
			}
			if a, dayAppts, ferr := apptSvc.FindBlockByAppointmentID(ctx, apptID); ferr == nil && a != nil && len(dayAppts) > 1 {
				apptJSON, _ := json.Marshal(dayAppts)
				listMsg := buildAppointmentList(ctx, apptSvc, dayAppts, procRepo)
				return sm.NewResult(sm.StateListAppointments).
					WithContext("appointments_json", string(apptJSON)).
					WithList("Tienes varias citas ese día. ¿Cuál deseas reprogramar?\n\n"+listMsg.body, listMsg.button, listMsg.section).
					WithEvent("notif_reschedule_select", map[string]interface{}{"count": len(dayAppts)}), nil
			}
			return sm.NewResult(sm.StateSearchSlots).
				WithEvent("notification_reschedule_confirmed", nil), nil
		default: // reschedule_no
			return sm.NewResult(sm.StateNotifPending).
				WithButtons(
					fmt.Sprintf(
						"Entendido, tu cita queda vigente.\n\n"+
							"📅 *Fecha:* %s\n🕐 *Hora:* %s\n💊 *Procedimiento:* %s\n\n¿Qué deseas hacer?",
						sess.GetContext("notif_appt_date"),
						sess.GetContext("notif_appt_time"),
						sess.GetContext("notif_cups_name"),
					),
					sm.Button{Text: "Confirmar cita", Payload: "confirm"},
					sm.Button{Text: "Reprogramar", Payload: "reschedule"},
					sm.Button{Text: "Cancelar cita", Payload: "cancel"},
				).
				WithEvent("notification_reschedule_declined", nil), nil
		}
	}
}

// confirmCancelNotifHandler handles CONFIRM_CANCEL_NOTIF state.
// Patient pressed "Cancelar" on the proactive confirmation template.
// We ask them to confirm (1/2) before executing the cancellation.
func confirmCancelNotifHandler(apptSvc *services.AppointmentService, onCancel CancellationCallback) sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		selected := sm.ValidatedPayload(ctx)
		switch selected {
		case "cancel_yes":
			// La cita en curso y las del día que guardó startConfirmCancelSession, si son de esta
			// cita (la sesión escalada no las trae: se toman del bloque, abajo).
			apptID := notifAppointmentID(sess)
			allIDs := notifAppointmentIDs(sess, apptID)

			// Cargar el bloque (citas) ANTES de cancelar: sirve (a) de fallback para los IDs y (b) para
			// obtener los (médico, agenda) liberados que se avisan a la lista de espera por slot.
			// FindBlockByAppointmentID acepta cualquier ID del grupo.
			if apptID == "" {
				apptID = sess.GetContext("reschedule_appt_id")
			}
			if apptID == "" && len(allIDs) > 0 {
				apptID = allIDs[0]
			}
			var freedBlock []domain.Appointment
			if apptID != "" {
				appt, block, err := apptSvc.FindBlockByAppointmentID(ctx, apptID)
				if err == nil && appt != nil {
					freedBlock = block
					if len(allIDs) == 0 {
						for _, a := range block {
							allIDs = append(allIDs, a.ID)
						}
					}
				} else if len(allIDs) == 0 {
					return buildAutoCloseResult("No pudimos encontrar tu cita. Por favor contacta a la clínica."), nil
				}
			}

			if err := apptSvc.CancelByIDs(ctx, allIDs, "Cancelada por paciente via WhatsApp", "whatsapp", sess.ConversationID); err != nil {
				return buildAutoCloseResult("No pudimos cancelar tu cita en este momento. Por favor intenta de nuevo.").
					WithEvent("notification_cancel_error", map[string]interface{}{"error": err.Error()}), nil
			}

			// Notify waiting list for freed slots (keyed by doctor+agenda, not by CUP).
			notifyFreedSlots(ctx, onCancel, freedBlock)

			return buildAutoCloseResult("Tu cita ha sido cancelada.\n\nSi deseas reprogramar, puedes escribirnos cuando lo necesites.").
				WithEvent("notification_cancel_confirmed", map[string]interface{}{
					"appointment_ids": allIDs,
					"total_cancelled": len(allIDs),
				}), nil
		default: // cancel_no
			return sm.NewResult(sm.StateNotifPending).
				WithButtons(
					fmt.Sprintf(
						"Entendido, tu cita queda vigente.\n\n"+
							"📅 *Fecha:* %s\n🕐 *Hora:* %s\n💊 *Procedimiento:* %s\n\n¿Qué deseas hacer?",
						sess.GetContext("notif_appt_date"),
						sess.GetContext("notif_appt_time"),
						sess.GetContext("notif_cups_name"),
					),
					sm.Button{Text: "Confirmar cita", Payload: "confirm"},
					sm.Button{Text: "Reprogramar", Payload: "reschedule"},
					sm.Button{Text: "Cancelar cita", Payload: "cancel"},
				).
				WithEvent("notification_cancel_declined", nil), nil
		}
	}
}

// notifPendingHandler handles NOTIF_PENDING state.
// Shows Confirmar/Reprogramar/Cancelar and routes to the appropriate sub-flow.
func notifPendingHandler(apptSvc *services.AppointmentService, procRepo repository.ProcedureRepository, addrMapper *services.AddressMapper) sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		selected := sm.ValidatedPayload(ctx)
		apptID := notifAppointmentID(sess)
		if apptID == "" {
			apptID = sess.GetContext("reschedule_appt_id")
		}

		switch selected {
		case "confirm":
			appt, _, err := apptSvc.FindBlockByAppointmentID(ctx, apptID)
			if err != nil || appt == nil {
				return buildAutoCloseResult("No pudimos encontrar tu cita. Por favor contacta a la clínica."), nil
			}
			allAppts, _ := apptSvc.GetPatientAppointmentsForDate(ctx, appt.PatientID, appt.Date)
			if len(allAppts) == 0 {
				allAppts = []domain.Appointment{*appt}
			}
			if err := apptSvc.ConfirmBlock(ctx, allAppts, "whatsapp_bot", sess.ConversationID); err != nil {
				return sm.NewResult(sm.StateNotifPending).
					WithText("No pudimos confirmar tu cita en este momento. Por favor intenta de nuevo.").
					WithEvent("notif_confirm_error", map[string]interface{}{"error": err.Error()}), nil
			}
			confirmMsg := buildNotifConfirmDetail(allAppts, appt, procRepo, addrMapper, ctx)
			return buildAutoCloseResult(confirmMsg).
				WithEvent("notif_pending_confirmed", map[string]interface{}{"appointment_id": apptID}), nil

		case "reschedule":
			a, dayAppts, ferr := apptSvc.FindBlockByAppointmentID(ctx, apptID)
			if ferr != nil || a == nil {
				return buildAutoCloseResult("No pudimos encontrar tu cita. Por favor contacta a la clínica."), nil //nolint:nilerr // se cierra con aviso al paciente, no se propaga
			}
			// La búsqueda se arma con la cita notificada, no con lo que traiga la sesión: la escalada
			// no trae paciente ni procedimiento, y una sesión reusada trae los de otro trámite.
			rc := apptSvc.RescheduleContext(ctx, a, len(dayAppts))
			// Si el paciente tiene varias citas ese día (notificadas juntas en un mismo mensaje),
			// no se puede reagendar "todas" a un mismo horario: se le deja elegir CUÁL reprogramar
			// (reutiliza la lista de "Mis citas", que arma el procedimiento de la elegida).
			if len(dayAppts) > 1 {
				apptJSON, _ := json.Marshal(dayAppts)
				listMsg := buildAppointmentList(ctx, apptSvc, dayAppts, procRepo)
				r := sm.NewResult(sm.StateListAppointments).
					WithContext("appointments_json", string(apptJSON))
				for _, k := range notifReschedulePatientKeys {
					r.WithContext(k, rc[k])
				}
				return r.WithClearCtx(staleRescheduleKeys(rc)...).
					WithList("Tienes varias citas ese día. ¿Cuál deseas reprogramar?\n\n"+listMsg.body, listMsg.button, listMsg.section).
					WithEvent("notif_reschedule_select", map[string]interface{}{"count": len(dayAppts)}), nil
			}
			if rc["cups_code"] == "" {
				return buildAutoCloseResult("No se puede reprogramar: no se encontró el procedimiento."), nil
			}
			r := sm.NewResult(sm.StateSearchSlots)
			for k, v := range rc {
				r.WithContext(k, v)
			}
			return r.WithClearCtx(staleRescheduleKeys(rc)...).
				WithEvent("notif_pending_reschedule", nil), nil

		default: // cancel
			return sm.NewResult(sm.StateConfirmCancelNotif).
				WithButtons(
					fmt.Sprintf(
						"¿Confirmas que deseas cancelar tu cita?\n\n"+
							"📅 *Fecha:* %s\n🕐 *Hora:* %s\n💊 *Procedimiento:* %s",
						sess.GetContext("notif_appt_date"),
						sess.GetContext("notif_appt_time"),
						sess.GetContext("notif_cups_name"),
					),
					sm.Button{Text: "Sí, cancelar", Payload: "cancel_yes"},
					sm.Button{Text: "No, mantener", Payload: "cancel_no"},
				), nil
		}
	}
}

// rescheduleFallbackQuestion es la pregunta de NOTIF_RESCHEDULE_FALLBACK: no hubo horarios para
// reprogramar y la cita original sigue vigente.
func rescheduleFallbackQuestion(cupsName string) string {
	return fmt.Sprintf("No hay horarios disponibles para *%s* en otra fecha.\n\nTu cita original sigue vigente. ¿Qué deseas hacer?", cupsName)
}

// notifReschedulePatientKeys son las claves del paciente de la cita notificada que fija la
// reprogramación también cuando el paciente elige la cita de una lista (las del procedimiento las
// arma «Mis citas» con la cita elegida).
var notifReschedulePatientKeys = []string{"patient_id", "patient_name", "patient_entity", "patient_contract", "patient_age"}

// staleRescheduleKeys son las claves que la búsqueda de la reprogramación leería y que la sesión
// pudo traer de otro trámite (las del grupo de procedimientos, más las de la preselección de lista
// de espera), sin las que rc vuelve a fijar: borrar y escribir la misma clave en un turno la
// dejaría borrada.
func staleRescheduleKeys(rc map[string]string) []string {
	keys := make([]string, 0, len(procedureGroupContextKeys)+2)
	for _, k := range append(slices.Clone(procedureGroupContextKeys), "from_waiting_list", "procedure_type") {
		if _, set := rc[k]; !set {
			keys = append(keys, k)
		}
	}
	return keys
}

// notifRescheduleFallbackHandler handles NOTIF_RESCHEDULE_FALLBACK state.
// When notification reschedule found no available slots, the patient can still
// confirm or cancel the original appointment.
func notifRescheduleFallbackHandler(apptSvc *services.AppointmentService, procRepo repository.ProcedureRepository, addrMapper *services.AddressMapper, onCancel CancellationCallback) sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		selected := sm.ValidatedPayload(ctx)
		apptID := sess.GetContext("reschedule_appt_id")

		switch selected {
		case "confirm":
			appt, _, err := apptSvc.FindBlockByAppointmentID(ctx, apptID)
			if err != nil || appt == nil {
				return buildAutoCloseResult("No pudimos encontrar tu cita. Por favor contacta a la clínica."), nil
			}

			allAppts, _ := apptSvc.GetPatientAppointmentsForDate(ctx, appt.PatientID, appt.Date)
			if len(allAppts) == 0 {
				allAppts = []domain.Appointment{*appt}
			}
			if err := apptSvc.ConfirmBlock(ctx, allAppts, "whatsapp_bot", sess.ConversationID); err != nil {
				return sm.NewResult(sm.StateNotifRescheduleFallback).
					WithText("No pudimos confirmar tu cita en este momento. Por favor intenta de nuevo.").
					WithEvent("notif_reschedule_confirm_error", map[string]interface{}{"error": err.Error()}), nil
			}

			// Build confirmation detail (same structure as handleConfirmation confirm)
			confirmMsg := buildNotifConfirmDetail(allAppts, appt, procRepo, addrMapper, ctx)

			return buildAutoCloseResult(confirmMsg).
				WithEvent("notif_reschedule_fallback_confirmed", map[string]interface{}{
					"appointment_id": apptID,
				}), nil

		default: // cancel
			appt, block, err := apptSvc.FindBlockByAppointmentID(ctx, apptID)
			if err != nil || appt == nil {
				return buildAutoCloseResult("No pudimos encontrar tu cita. Por favor contacta a la clínica."), nil
			}

			allIDs := make([]string, len(block))
			for i, a := range block {
				allIDs[i] = a.ID
			}
			if err := apptSvc.CancelByIDs(ctx, allIDs, "Cancelada por paciente via WhatsApp", "whatsapp", sess.ConversationID); err != nil {
				return sm.NewResult(sm.StateNotifRescheduleFallback).
					WithText("No pudimos cancelar tu cita en este momento. Por favor intenta de nuevo.").
					WithEvent("notif_reschedule_cancel_error", map[string]interface{}{"error": err.Error()}), nil
			}

			// Notify waiting list for freed slots (keyed by doctor+agenda, not by CUP).
			notifyFreedSlots(ctx, onCancel, block)

			return buildAutoCloseResult("Tu cita ha sido cancelada.\n\nSi deseas reprogramar, puedes escribirnos cuando lo necesites.").
				WithEvent("notif_reschedule_fallback_cancelled", map[string]interface{}{
					"appointment_id":  apptID,
					"total_cancelled": len(allIDs),
				}), nil
		}
	}
}

// buildNotifConfirmDetail builds the confirmation detail message with preparations and address.
func buildNotifConfirmDetail(allAppts []domain.Appointment, appt *domain.Appointment, procRepo repository.ProcedureRepository, addrMapper *services.AddressMapper, ctx context.Context) string {
	seen := make(map[string]bool)
	var procNames []string
	for _, a := range allAppts {
		for _, p := range a.Procedures {
			if p.CupName != "" && !seen[p.CupName] {
				seen[p.CupName] = true
				procNames = append(procNames, p.CupName)
			}
		}
	}
	proceduresText := strings.Join(procNames, " y ")
	if proceduresText == "" {
		proceduresText = "Procedimiento"
	}

	msg := fmt.Sprintf(
		"✅ ¡Tu cita ha sido confirmada!\n\n"+
			"*Fecha:* %s\n"+
			"*Hora:* %s\n"+
			"*Procedimiento:* %s",
		utils.FormatFriendlyDate(appt.Date),
		services.FormatTimeSlot(appt.TimeSlot),
		proceduresText,
	)

	if procRepo != nil {
		var prepText string
		address := ""
		mapsURL := ""
		seenCup := make(map[string]bool)
		for _, a := range allAppts {
			for _, proc := range a.Procedures {
				if proc.CupCode == "" || seenCup[proc.CupCode] {
					continue
				}
				seenCup[proc.CupCode] = true
				p, err := procRepo.FindByCode(ctx, utils.BaseCupCode(proc.CupCode))
				if err != nil || p == nil {
					continue
				}
				if address == "" && p.Address != "" {
					address = p.Address
					mapsURL = p.MapsURL
				}
				if p.Preparation != "" {
					prepText += fmt.Sprintf("\n- Para *%s*: %s", proc.CupName, p.Preparation)
					if p.VideoURL != "" {
						prepText += fmt.Sprintf("\n  📹 Video: %s", p.VideoURL)
					}
					if p.AudioURL != "" {
						prepText += fmt.Sprintf("\n  🎵 Audio: %s", p.AudioURL)
					}
				}
			}
		}
		if address != "" {
			if addrMapper != nil {
				msg += "\n" + addrMapper.FormatAddress(address, mapsURL)
			} else {
				msg += fmt.Sprintf("\n*Dirección:* %s", address)
			}
		}
		if prepText != "" {
			msg += "\n\n*Preparación:*" + prepText
		}
	}

	msg += "\n\nRecuerda presentarte 30 minutos antes para realizar el proceso de facturación. ¡Te esperamos!"
	return msg
}

// --- Helpers privados ---

// selectedAppointments devuelve solo la cita seleccionada (modelo 1 cita = N slots; ya no se
// agrupan citas consecutivas estilo Antares). Vacío si no se encuentra.
func selectedAppointments(appointments []domain.Appointment, selectedID string) []domain.Appointment {
	for i := range appointments {
		if appointments[i].ID == selectedID {
			return []domain.Appointment{appointments[i]}
		}
	}
	return nil
}

// executeConfirmAppointment realiza la confirmación de la cita, guardando el conversationID como medio.
// Busca preparaciones, video/audio y dirección con Maps URL para enviarlas al paciente.
func executeConfirmAppointment(ctx context.Context, sess *session.Session, apptSvc *services.AppointmentService, procRepo repository.ProcedureRepository, addrMapper *services.AddressMapper) (*sm.StateResult, error) {
	selectedID := sess.GetContext("selected_appointment_id")

	var appointments []domain.Appointment
	if err := json.Unmarshal([]byte(sess.GetContext("appointments_json")), &appointments); err != nil {
		return buildAutoCloseResult("No pudimos procesar tu cita en este momento."), nil
	}

	block := selectedAppointments(appointments, selectedID)
	if len(block) == 0 {
		return sm.NewResult(sm.StateListAppointments).
			WithText("No encontramos esa cita. Por favor selecciona otra de la lista.").
			WithClearCtx("selected_appointment_id"), nil
	}

	if err := apptSvc.ConfirmBlock(ctx, block, "whatsapp", sess.ConversationID); err != nil {
		return sm.NewResult(sm.StateAppointmentAction).
			WithText("No pudimos confirmar tu cita en este momento. Por favor intenta de nuevo.").
			WithEvent("appointment_confirm_error", map[string]interface{}{"error": err.Error()}), nil
	}

	confirmText := "Tu cita ha sido confirmada."
	if len(block) > 1 {
		confirmText = fmt.Sprintf("Tus %d citas han sido confirmadas.", len(block))
	}
	msg := fmt.Sprintf("✅ *¡Cita confirmada!*\n\n%s", confirmText)

	// Buscar preparaciones, video/audio y dirección del procedimiento
	if procRepo != nil {
		var prepText string
		address := ""
		mapsURL := ""
		for _, a := range block {
			for _, proc := range a.Procedures {
				if proc.CupCode == "" {
					continue
				}
				p, err := procRepo.FindByCode(ctx, utils.BaseCupCode(proc.CupCode))
				if err != nil || p == nil {
					continue
				}
				if address == "" && p.Address != "" {
					address = p.Address
					mapsURL = p.MapsURL
				}
				if p.Preparation != "" {
					prepText += fmt.Sprintf("\n• Para *%s*: %s", proc.CupName, p.Preparation)
					if p.VideoURL != "" {
						prepText += fmt.Sprintf("\n  📹 Ver video: %s", p.VideoURL)
					}
					if p.AudioURL != "" {
						prepText += fmt.Sprintf("\n  🎵 Audio: %s", p.AudioURL)
					}
				}
			}
		}
		if address != "" {
			if addrMapper != nil {
				msg += "\n" + addrMapper.FormatAddress(address, mapsURL)
			} else {
				msg += fmt.Sprintf("\n*Dirección:* %s", address)
			}
		}
		if prepText != "" {
			msg += "\n\n📋 *Preparación:*" + prepText
		}
	}

	msg += "\n\nRecuerda presentarte 30 minutos antes para realizar el proceso de facturación, con tu documento y orden médica."

	observability.Emit(observability.TraceSession(sess.ID), "mis_citas", "appt_confirmed",
		observability.EmitOpts{Phone: sess.PhoneNumber, RefID: sess.GetContext("selected_appointment_id")})
	return buildAutoCloseResult(msg).
		WithClearCtx("selected_appointment_id", "appointments_json").
		WithEvent("appointment_confirmed", map[string]interface{}{
			"appointment_id": selectedID,
			"block_size":     len(block),
		}), nil
}

// executeCancelAppointment realiza la cancelación de la cita.
func executeCancelAppointment(ctx context.Context, sess *session.Session, apptSvc *services.AppointmentService, onCancel CancellationCallback) (*sm.StateResult, error) {
	selectedID := sess.GetContext("selected_appointment_id")

	var appointments []domain.Appointment
	if err := json.Unmarshal([]byte(sess.GetContext("appointments_json")), &appointments); err != nil {
		return buildAutoCloseResult("No pudimos procesar tu cita en este momento."), nil
	}

	block := selectedAppointments(appointments, selectedID)
	if len(block) == 0 {
		return sm.NewResult(sm.StateListAppointments).
			WithText("No encontramos esa cita. Por favor selecciona otra de la lista.").
			WithClearCtx("selected_appointment_id"), nil
	}

	if err := apptSvc.CancelBlock(ctx, block, "Cancelada por paciente via WhatsApp", "whatsapp", sess.ConversationID); err != nil {
		return sm.NewResult(sm.StateAppointmentAction).
			WithText("No pudimos cancelar tu cita en este momento. Por favor intenta de nuevo.").
			WithEvent("appointment_cancel_error", map[string]interface{}{"error": err.Error()}), nil
	}

	// Notify waiting list for freed slots (keyed by doctor+agenda, not by CUP).
	notifyFreedSlots(ctx, onCancel, block)

	cancelText := "Tu cita ha sido cancelada."
	if len(block) > 1 {
		cancelText = fmt.Sprintf("Tus %d citas han sido canceladas.", len(block))
	}
	msg := cancelText

	observability.Emit(observability.TraceSession(sess.ID), "mis_citas", "appt_cancelled",
		observability.EmitOpts{Phone: sess.PhoneNumber, RefID: sess.GetContext("selected_appointment_id")})
	return buildAutoCloseResult(msg).
		WithClearCtx("selected_appointment_id", "appointments_json").
		WithEvent("appointment_cancelled", map[string]interface{}{
			"appointment_id": selectedID,
			"block_size":     len(block),
		}), nil
}

// backToAppointmentAction re-muestra el detalle de la cita + lista de acciones.
func backToAppointmentAction(ctx context.Context, sess *session.Session, apptSvc *services.AppointmentService, procRepo repository.ProcedureRepository) *sm.StateResult {
	selectedID := sess.GetContext("selected_appointment_id")
	var appointments []domain.Appointment
	json.Unmarshal([]byte(sess.GetContext("appointments_json")), &appointments)

	detail := buildAppointmentDetail(ctx, apptSvc, appointments, selectedID, procRepo)
	if detail == "" {
		return sm.NewResult(sm.StateListAppointments).
			WithText("Cita no encontrada. Selecciona otra.").
			WithClearCtx("selected_appointment_id")
	}

	return sm.NewResult(sm.StateAppointmentAction).
		WithList(
			detail+"\n\n¿Qué deseas hacer con esta cita?", "Ver opciones",
			sm.ListSection{Title: "Acciones", Rows: appointmentActionRows()},
		)
}

// showAppointmentPreparation looks up preparation instructions for the selected appointment's procedures.
func showAppointmentPreparation(ctx context.Context, sess *session.Session, apptSvc *services.AppointmentService, procRepo repository.ProcedureRepository, addrMapper *services.AddressMapper) (*sm.StateResult, error) {
	selectedID := sess.GetContext("selected_appointment_id")
	var appointments []domain.Appointment
	json.Unmarshal([]byte(sess.GetContext("appointments_json")), &appointments)

	block := selectedAppointments(appointments, selectedID)
	if len(block) == 0 {
		return sm.NewResult(sm.StateListAppointments).
			WithText("Cita no encontrada. Selecciona otra.").
			WithClearCtx("selected_appointment_id"), nil
	}

	if procRepo == nil {
		r := backToAppointmentAction(ctx, sess, apptSvc, procRepo)
		r.Messages = append([]sm.OutboundMessage{&sm.TextMessage{Text: "No se pudo consultar la preparación en este momento."}}, r.Messages...)
		return r, nil
	}

	var prepText string
	var address, mapsURL string
	seen := make(map[string]bool)
	for _, appt := range block {
		for _, proc := range appt.Procedures {
			if proc.CupCode == "" || seen[proc.CupCode] {
				continue
			}
			seen[proc.CupCode] = true
			p, err := procRepo.FindByCode(ctx, utils.BaseCupCode(proc.CupCode))
			if err != nil || p == nil {
				continue
			}
			if p.Preparation != "" {
				prepText += fmt.Sprintf("\n\n*%s:*\n%s", proc.CupName, p.Preparation)
			}
			if p.VideoURL != "" {
				prepText += fmt.Sprintf("\n📹 Ver video: %s", p.VideoURL)
			}
			if p.AudioURL != "" {
				prepText += fmt.Sprintf("\n🎵 Audio: %s", p.AudioURL)
			}
			if p.Address != "" && address == "" {
				address = p.Address
				mapsURL = p.MapsURL
			}
		}
	}

	var msg string
	if prepText == "" {
		msg = "No se encontraron instrucciones de preparación para esta cita."
	} else {
		msg = "📋 *Preparación para tu cita:*" + prepText
		if address != "" {
			if addrMapper != nil {
				msg += "\n\n" + addrMapper.FormatAddress(address, mapsURL)
			} else {
				msg += fmt.Sprintf("\n\n📍 *Dirección:* %s", address)
			}
		}
	}

	r := backToAppointmentAction(ctx, sess, apptSvc, procRepo)
	r.Messages = append([]sm.OutboundMessage{&sm.TextMessage{Text: msg}}, r.Messages...)
	return r.WithEvent("appointment_preparation_viewed", map[string]interface{}{
		"appointment_id": selectedID,
	}), nil
}

// buildAppointmentDetail construye el texto de detalle de una cita seleccionada.
func buildAppointmentDetail(ctx context.Context, apptSvc *services.AppointmentService, appointments []domain.Appointment, selectedID string, procRepo repository.ProcedureRepository) string {
	var appt *domain.Appointment
	for i, a := range appointments {
		if a.ID == selectedID {
			appt = &appointments[i]
			break
		}
	}

	if appt == nil {
		return ""
	}

	block := selectedAppointments(appointments, selectedID)

	statusText := "Pendiente"
	if appt.Confirmed {
		statusText = "Confirmada"
	}

	// N-43: propagar el ctx del handler en lugar de context.Background() para el lookup de procRepo.
	cupName := services.GetFirstCupName(*appt)
	if (cupName == "" || cupName == services.GetFirstCupCode(*appt)) && procRepo != nil {
		if p, err := procRepo.FindByCode(ctx, utils.BaseCupCode(services.GetFirstCupCode(*appt))); err == nil && p != nil && p.Name != "" {
			cupName = p.Name
		}
	}

	// DoctorName comes already resolved from SIESA (RTRIM(sis_medi.nombre)).
	doctorName := appt.DoctorName

	detail := fmt.Sprintf("*Detalle de tu cita:*\n\n"+
		"Procedimiento: %s\n"+
		"Doctor: %s\n"+
		"Fecha: %s\n"+
		"Hora: %s\n"+
		"Estado: %s",
		cupName,
		doctorName,
		utils.FormatFriendlyDate(appt.Date),
		services.FormatTimeSlot(appt.TimeSlot),
		statusText)

	if appt.Observations != "" {
		detail += fmt.Sprintf("\nObservaciones: %s", appt.Observations)
	}

	if len(block) > 1 {
		detail += fmt.Sprintf("\n\nEsta cita tiene *%d procedimientos consecutivos* que se gestionarán juntos.", len(block))
	}

	return detail
}

// appointmentActionRows retorna las filas de la lista de acciones para una cita.
func appointmentActionRows() []sm.ListRow {
	return []sm.ListRow{
		{ID: "appt_confirm", Title: "Confirmar cita", Description: "Confirmar asistencia a esta cita"},
		{ID: "appt_cancel", Title: "Cancelar cita", Description: "Cancelar esta cita"},
		{ID: "appt_reschedule", Title: "Reprogramar cita", Description: "Buscar nuevo horario para esta cita"},
		{ID: "appt_preparation", Title: "Ver preparación", Description: "Instrucciones de preparación para el examen"},
		{ID: "appt_back", Title: "Volver al listado", Description: "Ver otras citas programadas"},
		{ID: "appt_menu", Title: "Menú principal", Description: "Volver al menú principal"},
	}
}

// appointmentListData holds the data needed to build a WhatsApp list message
type appointmentListData struct {
	body    string
	button  string
	section sm.ListSection
}

// appointmentListMax es cuántas citas muestra la lista de LIST_APPOINTMENTS.
const appointmentListMax = 10

// appointmentListTitle es la fecha y hora de una fila de la lista de citas.
func appointmentListTitle(appt domain.Appointment) string {
	return fmt.Sprintf("%s %s", utils.FormatFriendlyDateShort(appt.Date), services.FormatTimeSlot(appt.TimeSlot))
}

// appointmentListCupName es el procedimiento de una fila de la lista de citas: el nombre de la
// cita, o el del catálogo si la cita solo trae el código.
func appointmentListCupName(ctx context.Context, appt domain.Appointment, procRepo repository.ProcedureRepository) string {
	cupName := services.GetFirstCupName(appt)
	cupCode := services.GetFirstCupCode(appt)
	if (cupName == "" || cupName == cupCode) && procRepo != nil {
		if p, err := procRepo.FindByCode(ctx, utils.BaseCupCode(cupCode)); err == nil && p != nil && p.Name != "" {
			cupName = p.Name
		}
	}
	return cupName
}

// appointmentReturnOptions arma las citas que el paciente ve en LIST_APPOINTMENTS (las mismas
// appointmentListMax primeras de appointments_json), con la fecha, el médico y el procedimiento
// de su fila. El token identifica la cita (cita_<ID>) y el Data es su ID, el payload de la fila
// que el handler reconoce como postback: una cita que ya no esté en la sesión deja el token
// desconocido y el paso vuelve a mostrar la lista.
//
// Se arma dentro del turno de escalación y sin su ctx: no consulta el catálogo de procedimientos;
// una cita que solo trae el código se rotula con el código.
func appointmentReturnOptions(sess *session.Session) []sm.ReturnOption {
	reload := sm.ReturnOption{
		Label: "Recargar citas desde el sistema", Value: "recargar_citas", Target: sm.StateFetchAppointments,
	}
	if sess == nil {
		return []sm.ReturnOption{reload}
	}
	var appts []domain.Appointment
	if err := json.Unmarshal([]byte(sess.GetContext("appointments_json")), &appts); err != nil {
		return []sm.ReturnOption{reload}
	}
	if len(appts) > appointmentListMax {
		appts = appts[:appointmentListMax]
	}
	options := make([]sm.ReturnOption, 0, len(appts)+1)
	for _, a := range appts {
		label := appointmentListTitle(a)
		if a.DoctorName != "" {
			label += " — Dr. " + a.DoctorName
		}
		cup := services.GetFirstCupName(a)
		if cup == "" {
			cup = services.GetFirstCupCode(a)
		}
		if cup != "" {
			label += " — " + cup
		}
		options = append(options, sm.ReturnOption{Label: label, Value: "cita_" + returnTokenPart(a.ID), Data: a.ID})
	}
	return append(options, reload)
}

// buildAppointmentList constructs the list display for appointments.
// Each appointment is shown as its own row (modelo 1 cita = N slots; sin agrupar citas
// consecutivas estilo Antares). Las acciones de confirmar/cancelar actúan solo sobre la
// cita seleccionada vía selectedAppointments.
func buildAppointmentList(ctx context.Context, _ *services.AppointmentService, appointments []domain.Appointment, procRepo repository.ProcedureRepository) appointmentListData {
	maxShow := appointmentListMax
	// N-43: propagar el ctx del handler en lugar de context.Background() para el lookup de procRepo.

	rows := make([]sm.ListRow, 0, maxShow)
	for _, appt := range appointments {
		cupName := appointmentListCupName(ctx, appt, procRepo)
		// DoctorName comes already resolved from SIESA (RTRIM(sis_medi.nombre)).
		doctorName := appt.DoctorName
		title := appointmentListTitle(appt)
		desc := fmt.Sprintf("Dr. %s - %s", doctorName, cupName)

		rows = append(rows, sm.ListRow{
			ID:          appt.ID,
			Title:       truncate(title, 24),
			Description: truncate(desc, 72),
		})

		if len(rows) >= maxShow {
			break
		}
	}

	body := "Tienes *1 cita* programada."
	if len(appointments) > 1 {
		body = fmt.Sprintf("Tienes *%d citas* programadas.", len(appointments))
	}
	if len(appointments) > maxShow {
		body += fmt.Sprintf("\nMostrando las primeras %d:", maxShow)
	} else {
		body += "\nSelecciona una para ver detalles:"
	}

	return appointmentListData{
		body:    body,
		button:  "Ver citas",
		section: sm.ListSection{Title: "Tus citas", Rows: rows},
	}
}

// truncate corta un string a maxLen caracteres
func truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen-1]) + "…"
}

// notifAppointmentID es la cita de la notificación en curso. La sesión escalada de una
// notificación (escalateToAgent y escalateNotifToAgent en internal/notifications) la guarda como
// notif_appointment_id; la de confirmación/cancelación desde la plantilla, como notif_appt_id.
// Manda notif_appointment_id: si la escalación reusó una sesión que ya traía otra cita, la que
// escribió la escalación es la de los botones que ven el agente y el paciente.
func notifAppointmentID(sess *session.Session) string {
	if id := sess.GetContext("notif_appointment_id"); id != "" {
		return id
	}
	return sess.GetContext("notif_appt_id")
}

// notifAppointmentIDs son las citas del día que startConfirmCancelSession guardó en
// notif_appt_ids, solo si incluyen apptID: la sesión escalada no las guarda, y unas que no
// incluyen la cita en curso son de otra notificación. Sin ellas, quien llama usa el bloque del día.
func notifAppointmentIDs(sess *session.Session, apptID string) []string {
	var ids []string
	if raw := sess.GetContext("notif_appt_ids"); raw != "" {
		_ = json.Unmarshal([]byte(raw), &ids)
	}
	if apptID != "" && !slices.Contains(ids, apptID) {
		return nil
	}
	return ids
}

// notifPendingKind es la clase de notificación escalada en NOTIF_PENDING: decide las acciones del
// agente y si los 3 botones del paso (confirmar, reprogramar, cancelar) le corresponden.
type notifPendingKind int

const (
	// notifKindAppointment: confirmación o reprogramación de una cita (y sin sesión).
	notifKindAppointment notifPendingKind = iota
	// notifKindSystemFailure: el paciente YA confirmó y falló la persistencia en SIESA.
	notifKindSystemFailure
	notifKindWaitingList
	// notifKindCancellation: la clínica canceló la cita.
	notifKindCancellation
)

// classifyNotifPending lee la clase de notificación de la sesión (nil = la de una cita).
func classifyNotifPending(sess *session.Session) notifPendingKind {
	if sess == nil {
		return notifKindAppointment
	}
	if sess.GetContext(sm.CtxNotifEscalationDetail) == sm.EscalationReasonNotifSystemFailure {
		return notifKindSystemFailure
	}
	switch sess.GetContext("notif_type") {
	case "waiting_list":
		return notifKindWaitingList
	case "cancellation":
		return notifKindCancellation
	}
	return notifKindAppointment
}

// notifPendingButtonsDontApply dice si los 3 botones de NOTIF_PENDING NO corresponden a la
// notificación de la sesión: la lista de espera y la cancelación de la clínica tienen otras
// acciones, y tras el fallo de SIESA el paciente ya confirmó. Ahí el agente elige la acción del
// menú y no se le ofrece reanudar tal cual.
func notifPendingButtonsDontApply(sess *session.Session) bool {
	return classifyNotifPending(sess) != notifKindAppointment
}

// notifPendingReturnOptions son las acciones que el agente puede tomar sobre una notificación
// escalada, según su clase (classifyNotifPending). Cada `Data` es una acción que el atajo de
// NOTIF_PENDING del worker entrega a HandleNotifPendingCommand. Sin sesión sale el menú de la
// confirmación, el de siempre.
func notifPendingReturnOptions(sess *session.Session) []sm.ReturnOption {
	np := sm.StateNotifPending
	action := func(label, value string) sm.ReturnOption {
		return sm.ReturnOption{Label: label, Value: value, Target: np, Data: value}
	}
	switch classifyNotifPending(sess) {
	case notifKindSystemFailure:
		// Re-pedirle confirmación no sirve: el agente reintenta, o la confirma a mano y cierra.
		return []sm.ReturnOption{
			action("Reintentar la confirmación en SIESA", "confirm"),
			{Label: "Ya la confirmé a mano en SIESA", Value: "confirmada_manual", Close: true},
		}
	case notifKindWaitingList:
		return []sm.ReturnOption{action("Agendar ahora", "wl_schedule"), action("No le interesa", "wl_decline")}
	case notifKindCancellation:
		return []sm.ReturnOption{action("Entendido, sin reprogramar", "understood"), action("Reprogramar", "reschedule")}
	}
	return []sm.ReturnOption{
		action("Confirmar la cita", "confirm"),
		action("Reprogramar la cita", "reschedule"),
		action("Cancelar la cita", "cancel"),
	}
}
