package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/repository"
	"github.com/neuro-bot/neuro-bot/internal/services"
	"github.com/neuro-bot/neuro-bot/internal/session"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
	"github.com/neuro-bot/neuro-bot/internal/utils"
)

// CancellationCallback is called after a patient cancels an appointment via the bot.
// cupsCode is the CUPS code of the freed slot (called once per unique CUPS in the block).
type CancellationCallback func(ctx context.Context, cupsCode string)

// RegisterAppointmentHandlers registra los handlers del flujo de consulta de citas.
func RegisterAppointmentHandlers(m *sm.Machine, apptSvc *services.AppointmentService, procRepo repository.ProcedureRepository, doctorRepo repository.DoctorRepository, addrMapper *services.AddressMapper, onCancel CancellationCallback) {
	m.Register(sm.StateFetchAppointments, fetchAppointmentsHandler(apptSvc, procRepo, doctorRepo))
	m.Register(sm.StateListAppointments, listAppointmentsHandler(apptSvc, procRepo, doctorRepo))
	m.Register(sm.StateAppointmentAction, appointmentActionHandler(apptSvc, procRepo, doctorRepo, addrMapper))
	m.Register(sm.StateConfirmAppointment, confirmAppointmentHandler(apptSvc, procRepo, doctorRepo, addrMapper))
	m.Register(sm.StateCancelAppointment, cancelAppointmentHandler(apptSvc, procRepo, doctorRepo, onCancel))
	m.Register(sm.StateNoAppointments, noAppointmentsHandler())

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
	m.RegisterWithConfig(sm.StateConfirmRescheduleNotif, sm.HandlerConfig{
		InputType:   sm.InputButton,
		Options:     []string{"reschedule_yes", "reschedule_no"},
		ErrorMsg:    "Por favor selecciona una de las opciones.",
		RetryPrompt: confirmReschedulePrompt,
		Handler:     confirmRescheduleNotifHandler(),
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
	m.RegisterWithConfig(sm.StateNotifPending, sm.HandlerConfig{
		InputType:   sm.InputButton,
		Options:     []string{"confirm", "reschedule", "cancel"},
		ErrorMsg:    "Por favor selecciona una opción.",
		RetryPrompt: notifPendingPrompt,
		Handler:     notifPendingHandler(apptSvc, procRepo, addrMapper),
	})

	// Fallback: reschedule sin slots → ofrece Confirmar/Cancelar
	m.RegisterWithConfig(sm.StateNotifRescheduleFallback, sm.HandlerConfig{
		InputType: sm.InputButton,
		Options:   []string{"confirm", "cancel"},
		ErrorMsg:  "Por favor selecciona una opción.",
		Handler:   notifRescheduleFallbackHandler(apptSvc, procRepo, addrMapper, onCancel),
	})
}

// FETCH_APPOINTMENTS (automático) — consulta citas del paciente y muestra la lista
func fetchAppointmentsHandler(apptSvc *services.AppointmentService, procRepo repository.ProcedureRepository, doctorRepo repository.DoctorRepository) sm.StateHandler {
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
		listMsg := buildAppointmentList(apptSvc, appointments, procRepo, doctorRepo)

		return sm.NewResult(sm.StateListAppointments).
			WithContext("appointments_json", string(apptJSON)).
			WithList(listMsg.body, listMsg.button, listMsg.section).
			WithEvent("appointments_found", map[string]interface{}{"count": len(appointments)}), nil
	}
}

// LIST_APPOINTMENTS (interactivo, lista) — espera selección de cita, muestra detalle al seleccionar
func listAppointmentsHandler(apptSvc *services.AppointmentService, procRepo repository.ProcedureRepository, doctorRepo repository.DoctorRepository) sm.StateHandler {
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
					detail := buildAppointmentDetail(apptSvc, appts, a.ID, procRepo, doctorRepo)
					return sm.NewResult(sm.StateAppointmentAction).
						WithContext("selected_appointment_id", msg.PostbackPayload).
						WithList(detail+"\n\n¿Qué deseas hacer con esta cita?", "Ver opciones",
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
				detail := buildAppointmentDetail(apptSvc, appts, selected.ID, procRepo, doctorRepo)
				return sm.NewResult(sm.StateAppointmentAction).
					WithContext("selected_appointment_id", selected.ID).
					WithList(detail+"\n\n¿Qué deseas hacer con esta cita?", "Ver opciones",
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

		listMsg := buildAppointmentList(apptSvc, appointments, procRepo, doctorRepo)

		return sm.NewResult(sess.CurrentState).
			WithList(listMsg.body, listMsg.button, listMsg.section).
			WithEvent("appointments_listed", map[string]interface{}{"shown": len(listMsg.section.Rows)}), nil
	}
}

// APPOINTMENT_ACTION (interactivo, lista) — procesa acción seleccionada sobre la cita
func appointmentActionHandler(apptSvc *services.AppointmentService, procRepo repository.ProcedureRepository, doctorRepo repository.DoctorRepository, addrMapper *services.AddressMapper) sm.StateHandler {
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

			detail := buildAppointmentDetail(apptSvc, appointments, selectedID, procRepo, doctorRepo)
			if detail == "" {
				return sm.NewResult(sm.StateListAppointments).
					WithText("Cita no encontrada. Selecciona otra.").
					WithClearCtx("selected_appointment_id"), nil
			}

			return sm.NewResult(sess.CurrentState).
				WithList(detail+"\n\n¿Qué deseas hacer con esta cita?", "Ver opciones",
					sm.ListSection{Title: "Acciones", Rows: appointmentActionRows()},
				), nil
		}

		switch selected {
		case "appt_confirm":
			return sm.NewResult(sm.StateConfirmAppointment).
				WithButtons("¿Estás seguro de *confirmar* esta cita?",
					sm.Button{Text: "Sí, confirmar", Payload: "confirm_yes"},
					sm.Button{Text: "No, volver", Payload: "confirm_no"},
				).
				WithEvent("appointment_confirm_requested", nil), nil

		case "appt_cancel":
			return sm.NewResult(sm.StateCancelAppointment).
				WithButtons("¿Estás seguro de *cancelar* esta cita? Esta acción no se puede deshacer.",
					sm.Button{Text: "Sí, cancelar", Payload: "cancel_yes"},
					sm.Button{Text: "No, volver", Payload: "cancel_no"},
				).
				WithEvent("appointment_cancel_requested", nil), nil

		case "appt_reschedule":
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

			isContrasted := "0"
			if strings.Contains(selectedAppt.Observations, "Contrastada") {
				isContrasted = "1"
			}
			isSedated := "0"
			if strings.Contains(selectedAppt.Observations, "Sedaci") {
				isSedated = "1"
			}

			block := apptSvc.FindConsecutiveBlock(appointments, selectedID)
			espacios := len(block)
			if espacios == 0 {
				espacios = 1
			}
			// Para resonancias: recalcular espacios según código y contraste en lugar del bloque anterior
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

			return sm.NewResult(sm.StateSearchSlots).
				WithContext("cups_code", cupsCode).
				WithContext("cups_name", cupsName).
				WithContext("is_contrasted", isContrasted).
				WithContext("is_sedated", isSedated).
				WithContext("espacios", fmt.Sprintf("%d", espacios)).
				WithContext("preferred_doctor_doc", selectedAppt.DoctorID).
				WithContext("total_procedures", "1").
				WithContext("current_procedure_idx", "0").
				WithContext("reschedule_appt_id", selectedAppt.ID).
				WithContext("patient_age", "0").
				WithContext("procedures_json", string(proceduresJSON)).
				WithText("Buscando horarios disponibles para reprogramar tu cita de *"+cupsName+"*...").
				WithEvent("appointment_reschedule_started", map[string]interface{}{
					"old_appt_id": selectedAppt.ID,
					"cups_code":   cupsCode,
				}), nil

		case "appt_preparation":
			return showAppointmentPreparation(ctx, sess, apptSvc, procRepo, doctorRepo, addrMapper)

		case "appt_back":
			var appointments []domain.Appointment
			if err := json.Unmarshal([]byte(sess.GetContext("appointments_json")), &appointments); err != nil {
				slog.Error("appointments_unmarshal_failed", "error", err, "location", "appointmentAction_back")
				return buildAutoCloseResult("No pudimos cargar tus citas. Por favor intenta de nuevo.").
					WithEvent("appointments_unmarshal_error", map[string]interface{}{"error": err.Error()}), nil
			}
			listMsg := buildAppointmentList(apptSvc, appointments, procRepo, doctorRepo)

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
func confirmAppointmentHandler(apptSvc *services.AppointmentService, procRepo repository.ProcedureRepository, doctorRepo repository.DoctorRepository, addrMapper *services.AddressMapper) sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		result, selected := sm.ValidateButtonResponse(sess, msg, "confirm_yes", "confirm_no")
		if result != nil {
			if result.NextState == sm.StateEscalateToAgent {
				return result, nil
			}
			result.Messages = nil
			return sm.NewResult(sess.CurrentState).
				WithButtons("¿Estás seguro de *confirmar* esta cita?",
					sm.Button{Text: "Sí, confirmar", Payload: "confirm_yes"},
					sm.Button{Text: "No, volver", Payload: "confirm_no"},
				), nil
		}

		switch selected {
		case "confirm_yes":
			return executeConfirmAppointment(ctx, sess, apptSvc, procRepo, addrMapper)

		case "confirm_no":
			// Volver al detalle de la cita
			return backToAppointmentAction(sess, apptSvc, procRepo, doctorRepo), nil
		}

		return nil, fmt.Errorf("unreachable: selected=%s", selected)
	}
}

// CANCEL_APPOINTMENT (interactivo) — reconfirmación antes de cancelar la cita.
func cancelAppointmentHandler(apptSvc *services.AppointmentService, procRepo repository.ProcedureRepository, doctorRepo repository.DoctorRepository, onCancel CancellationCallback) sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		result, selected := sm.ValidateButtonResponse(sess, msg, "cancel_yes", "cancel_no")
		if result != nil {
			if result.NextState == sm.StateEscalateToAgent {
				return result, nil
			}
			result.Messages = nil
			return sm.NewResult(sess.CurrentState).
				WithButtons("¿Estás seguro de *cancelar* esta cita? Esta acción no se puede deshacer.",
					sm.Button{Text: "Sí, cancelar", Payload: "cancel_yes"},
					sm.Button{Text: "No, volver", Payload: "cancel_no"},
				), nil
		}

		switch selected {
		case "cancel_yes":
			return executeCancelAppointment(ctx, sess, apptSvc, onCancel)

		case "cancel_no":
			return backToAppointmentAction(sess, apptSvc, procRepo, doctorRepo), nil
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
				WithButtons("No tienes citas pendientes o confirmadas.\n\n¿Qué deseas hacer?",
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
func confirmRescheduleNotifHandler() sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		selected := sm.ValidatedPayload(ctx)
		switch selected {
		case "reschedule_yes":
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
			// Get all appointment IDs stored by startConfirmCancelSession
			allIDsJSON := sess.GetContext("notif_appt_ids")
			var allIDs []string
			if allIDsJSON != "" {
				json.Unmarshal([]byte(allIDsJSON), &allIDs)
			}

			// Fallback: if no batch IDs, use the single appointment block
			if len(allIDs) == 0 {
				apptID := sess.GetContext("notif_appt_id")
				if apptID == "" {
					apptID = sess.GetContext("reschedule_appt_id")
				}
				appt, block, err := apptSvc.FindBlockByAppointmentID(ctx, apptID)
				if err != nil || appt == nil {
					return buildAutoCloseResult("No pudimos encontrar tu cita. Por favor contacta a la clínica."), nil
				}
				for _, a := range block {
					allIDs = append(allIDs, a.ID)
				}
			}

			if err := apptSvc.CancelByIDs(ctx, allIDs, "Cancelada por paciente via WhatsApp", "whatsapp", sess.ConversationID); err != nil {
				return buildAutoCloseResult("No pudimos cancelar tu cita en este momento. Por favor intenta de nuevo.").
					WithEvent("notification_cancel_error", map[string]interface{}{"error": err.Error()}), nil
			}

			// Notify waiting list for freed CUPS codes (stored in session context before cancel)
			if onCancel != nil {
				cupsJSON := sess.GetContext("notif_cups_codes")
				if cupsJSON != "" {
					var cupsCodes []string
					json.Unmarshal([]byte(cupsJSON), &cupsCodes)
					for _, code := range cupsCodes {
						go onCancel(ctx, code)
					}
				}
			}

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
		apptID := sess.GetContext("notif_appt_id")
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
			return sm.NewResult(sm.StateSearchSlots).
				WithContext("reschedule_appt_id", apptID).
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

			// Notify waiting list for freed CUPS codes
			if onCancel != nil {
				seen := make(map[string]bool)
				for _, a := range block {
					for _, p := range a.Procedures {
						if p.CupCode != "" && !seen[p.CupCode] {
							seen[p.CupCode] = true
							go onCancel(ctx, p.CupCode)
						}
					}
				}
			}

			return buildAutoCloseResult("Tu cita ha sido cancelada.\n\nSi deseas reprogramar, puedes escribirnos cuando lo necesites.").
				WithEvent("notif_reschedule_fallback_cancelled", map[string]interface{}{
					"appointment_id": apptID,
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

	msg := fmt.Sprintf("✅ ¡Tu cita ha sido confirmada!\n\n"+
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
				msg += "\n" + addrMapper.FormatAddress(address)
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

// executeConfirmAppointment realiza la confirmación de la cita, guardando el conversationID como medio.
// Busca preparaciones, video/audio y dirección con Maps URL para enviarlas al paciente.
func executeConfirmAppointment(ctx context.Context, sess *session.Session, apptSvc *services.AppointmentService, procRepo repository.ProcedureRepository, addrMapper *services.AddressMapper) (*sm.StateResult, error) {
	selectedID := sess.GetContext("selected_appointment_id")

	var appointments []domain.Appointment
	if err := json.Unmarshal([]byte(sess.GetContext("appointments_json")), &appointments); err != nil {
		return buildAutoCloseResult("No pudimos procesar tu cita en este momento."), nil
	}

	block := apptSvc.FindConsecutiveBlock(appointments, selectedID)
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
				msg += "\n" + addrMapper.FormatAddress(address)
			} else {
				msg += fmt.Sprintf("\n*Dirección:* %s", address)
			}
		}
		if prepText != "" {
			msg += "\n\n📋 *Preparación:*" + prepText
		}
	}

	msg += "\n\nRecuerda presentarte 30 minutos antes para realizar el proceso de facturación, con tu documento y orden médica."

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

	block := apptSvc.FindConsecutiveBlock(appointments, selectedID)
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

	// Notify waiting list for freed CUPS codes
	if onCancel != nil {
		seen := make(map[string]bool)
		for _, appt := range block {
			for _, proc := range appt.Procedures {
				if proc.CupCode != "" && !seen[proc.CupCode] {
					seen[proc.CupCode] = true
					go onCancel(ctx, proc.CupCode)
				}
			}
		}
	}

	cancelText := "Tu cita ha sido cancelada."
	if len(block) > 1 {
		cancelText = fmt.Sprintf("Tus %d citas han sido canceladas.", len(block))
	}
	msg := cancelText

	return buildAutoCloseResult(msg).
		WithClearCtx("selected_appointment_id", "appointments_json").
		WithEvent("appointment_cancelled", map[string]interface{}{
			"appointment_id": selectedID,
			"block_size":     len(block),
		}), nil
}

// backToAppointmentAction re-muestra el detalle de la cita + lista de acciones.
func backToAppointmentAction(sess *session.Session, apptSvc *services.AppointmentService, procRepo repository.ProcedureRepository, doctorRepo repository.DoctorRepository) *sm.StateResult {
	selectedID := sess.GetContext("selected_appointment_id")
	var appointments []domain.Appointment
	json.Unmarshal([]byte(sess.GetContext("appointments_json")), &appointments)

	detail := buildAppointmentDetail(apptSvc, appointments, selectedID, procRepo, doctorRepo)
	if detail == "" {
		return sm.NewResult(sm.StateListAppointments).
			WithText("Cita no encontrada. Selecciona otra.").
			WithClearCtx("selected_appointment_id")
	}

	return sm.NewResult(sm.StateAppointmentAction).
		WithList(detail+"\n\n¿Qué deseas hacer con esta cita?", "Ver opciones",
			sm.ListSection{Title: "Acciones", Rows: appointmentActionRows()},
		)
}

// showAppointmentPreparation looks up preparation instructions for the selected appointment's procedures.
func showAppointmentPreparation(ctx context.Context, sess *session.Session, apptSvc *services.AppointmentService, procRepo repository.ProcedureRepository, doctorRepo repository.DoctorRepository, addrMapper *services.AddressMapper) (*sm.StateResult, error) {
	selectedID := sess.GetContext("selected_appointment_id")
	var appointments []domain.Appointment
	json.Unmarshal([]byte(sess.GetContext("appointments_json")), &appointments)

	block := apptSvc.FindConsecutiveBlock(appointments, selectedID)
	if len(block) == 0 {
		return sm.NewResult(sm.StateListAppointments).
			WithText("Cita no encontrada. Selecciona otra.").
			WithClearCtx("selected_appointment_id"), nil
	}

	if procRepo == nil {
		r := backToAppointmentAction(sess, apptSvc, procRepo, doctorRepo)
		r.Messages = append([]sm.OutboundMessage{&sm.TextMessage{Text: "No se pudo consultar la preparación en este momento."}}, r.Messages...)
		return r, nil
	}

	var prepText string
	var address string
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
				msg += "\n\n" + addrMapper.FormatAddress(address)
			} else {
				msg += fmt.Sprintf("\n\n📍 *Dirección:* %s", address)
			}
		}
	}

	r := backToAppointmentAction(sess, apptSvc, procRepo, doctorRepo)
	r.Messages = append([]sm.OutboundMessage{&sm.TextMessage{Text: msg}}, r.Messages...)
	return r.WithEvent("appointment_preparation_viewed", map[string]interface{}{
		"appointment_id": selectedID,
	}), nil
}

// buildAppointmentDetail construye el texto de detalle de una cita seleccionada.
func buildAppointmentDetail(apptSvc *services.AppointmentService, appointments []domain.Appointment, selectedID string, procRepo repository.ProcedureRepository, doctorRepo repository.DoctorRepository) string {
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

	block := apptSvc.FindConsecutiveBlock(appointments, selectedID)

	statusText := "Pendiente"
	if appt.Confirmed {
		statusText = "Confirmada"
	}

	ctx := context.Background()

	cupName := services.GetFirstCupName(*appt)
	if (cupName == "" || cupName == services.GetFirstCupCode(*appt)) && procRepo != nil {
		if p, err := procRepo.FindByCode(ctx, utils.BaseCupCode(services.GetFirstCupCode(*appt))); err == nil && p != nil && p.Name != "" {
			cupName = p.Name
		}
	}

	doctorName := appt.DoctorName
	if doctorRepo != nil && appt.DoctorDocument != "" {
		if doc, err := doctorRepo.FindByDocument(ctx, appt.DoctorDocument); err == nil && doc != nil && doc.FullName != "" {
			doctorName = doc.FullName
		}
	}

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

// buildAppointmentList constructs the list display for appointments.
// Each appointment is shown as its own row (no block grouping for display).
// Block grouping is only used in confirm/cancel actions via FindConsecutiveBlock.
func buildAppointmentList(apptSvc *services.AppointmentService, appointments []domain.Appointment, procRepo repository.ProcedureRepository, doctorRepo repository.DoctorRepository) appointmentListData {
	maxShow := 10
	ctx := context.Background()

	rows := make([]sm.ListRow, 0, maxShow)
	for _, appt := range appointments {
		cupName := services.GetFirstCupName(appt)
		cupCode := services.GetFirstCupCode(appt)
		if (cupName == "" || cupName == cupCode) && procRepo != nil {
			if p, err := procRepo.FindByCode(ctx, utils.BaseCupCode(cupCode)); err == nil && p != nil && p.Name != "" {
				cupName = p.Name
			}
		}
		doctorName := appt.DoctorName
		if doctorRepo != nil && appt.DoctorDocument != "" {
			if doc, err := doctorRepo.FindByDocument(ctx, appt.DoctorDocument); err == nil && doc != nil && doc.FullName != "" {
				doctorName = doc.FullName
			}
		}
		title := fmt.Sprintf("%s %s", utils.FormatFriendlyDateShort(appt.Date), services.FormatTimeSlot(appt.TimeSlot))
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
