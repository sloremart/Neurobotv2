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
	"time"

	"github.com/google/uuid"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/observability"
	"github.com/neuro-bot/neuro-bot/internal/repository"
	"github.com/neuro-bot/neuro-bot/internal/services"
	"github.com/neuro-bot/neuro-bot/internal/session"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
	"github.com/neuro-bot/neuro-bot/internal/utils"
)

// RegisterMedicalOrderHandlers registra los handlers de Orden Médica y OCR (Fase 8).
// apptSvc habilita la consolidación de órdenes EMG/NC (puede ser nil: en ese caso, ante una orden
// dependiente-sola, el flujo pide la orden de la EMG en lugar de consolidar).
func RegisterMedicalOrderHandlers(m *sm.Machine, ocrSvc *services.OCRService, procedureRepo repository.ProcedureRepository, birdClient *bird.Client, wlRepo WaitingListCreator, apptSvc *services.AppointmentService) {
	m.Register(sm.StateAskMedicalOrder, askMedicalOrderHandler(wlRepo, ocrSvc, birdClient))
	m.Register(sm.StateSelectWaitingList, selectWaitingListHandler(wlRepo))
	m.Register(sm.StateUploadMedicalOrder, uploadMedicalOrderHandler(ocrSvc, birdClient))
	// La segunda lectura del código la hace el OCR; sin él (nil) se salta ese paso.
	var cupsReader CupsCodeReader
	if ocrSvc != nil {
		cupsReader = ocrSvc
	}
	m.Register(sm.StateValidateOCR, validateOCRHandler(procedureRepo, cupsReader))
	m.Register(sm.StateConfirmOCRResult, confirmOCRResultHandler(procedureRepo, birdClient))
	m.Register(sm.StateOCRFailed, ocrFailedHandler(birdClient))
	m.Register(sm.StateAskManualCups, askManualCupsHandler(procedureRepo))
	m.Register(sm.StateSelectProcedure, selectProcedureHandler())
	sm.RegisterReturnMenu(sm.StateSelectProcedure, procedureReturnOptions)
	sm.RegisterResolveGuard(sm.StateSelectProcedure, sm.ResolveGuardPendingReturn)

	// Retorno del agente de Néctar en los pasos de la orden médica: escribe los CUPS que leyó de
	// la orden y el comando de CUPS del asesor (el de `/bot cups`) los inyecta en VALIDATE_OCR.
	// Ningún paso sabría leer ese texto: lo mandaría de nuevo a escalar.
	for _, step := range []string{
		sm.StateAskMedicalOrder, sm.StateUploadMedicalOrder, sm.StateValidateOCR,
		sm.StateOCRFailed, sm.StateConfirmOCRResult, sm.StateAskManualCups,
	} {
		sm.RegisterReturnInput(step, agentCupsReturnInput())
		sm.RegisterResumeLabel(step, agentCupsResumeLabel)
		// Sin los CUPS leídos de la orden el flujo no sigue: queda esperando el retorno del agente.
		sm.RegisterResolveGuard(step, sm.ResolveGuardPendingReturn)
	}
	// Reintentar la subida de la foto (`/bot resume UPLOAD_MEDICAL_ORDER`): no lleva dato.
	askOrderAgain := sm.ReturnOption{
		Label: "Pedir de nuevo la foto de la orden", Value: "pedir_orden",
		Target: sm.StateUploadMedicalOrder, NoInput: true,
	}
	for _, step := range []string{
		sm.StateAskMedicalOrder, sm.StateUploadMedicalOrder, sm.StateValidateOCR, sm.StateOCRFailed,
	} {
		sm.RegisterStaticReturnMenu(step, askOrderAgain)
	}
	// En la confirmación de lo leído, el agente también contesta los botones del paciente.
	sm.RegisterStaticReturnMenu(sm.StateConfirmOCRResult,
		askOrderAgain,
		noInputPayloadOption("Los procedimientos leídos son correctos", "ocr_correct"),
		noInputPayloadOption("Los procedimientos leídos están mal", "ocr_incorrect"),
	)

	// Consolidación EMG/NC entre órdenes separadas.
	m.Register(sm.StateCheckEmgConsolidation, checkEmgConsolidationHandler(apptSvc))
	m.Register(sm.StateConfirmConsolidate, confirmConsolidateHandler(apptSvc))
	sm.RegisterStaticReturnMenu(sm.StateConfirmConsolidate,
		payloadOption("Sí, unir con la cita existente", "consolidate_yes"),
		payloadOption("No, agendar aparte", "consolidate_no"),
	)
	m.Register(sm.StateAskEmgOrder, askEmgOrderHandler())
	sm.RegisterStaticReturnMenu(sm.StateAskEmgOrder,
		payloadOption("Tiene la orden de electromiografía", "emg_order_yes"),
		payloadOption("No tiene la orden", "emg_order_no"),
	)
	m.Register(sm.StateUploadEmgOrder, uploadEmgOrderHandler(ocrSvc, birdClient))
	// Los CUPS escritos aquí son los de la orden de EMG: en Néctar el comando de CUPS los suma a
	// los dependientes de la 1ª orden (emg_dep_cups_json) si los hay, como el handler con la foto (C5).
	emgCups := agentCupsReturnInput()
	emgCups.Label = "CUPS de la orden de electromiografía (si ya se leyeron dependientes de la 1ª orden, se suman)"
	sm.RegisterReturnInput(sm.StateUploadEmgOrder, emgCups)
	sm.RegisterResumeLabel(sm.StateUploadEmgOrder, agentCupsResumeLabel)
	sm.RegisterResolveGuard(sm.StateUploadEmgOrder, sm.ResolveGuardPendingReturn)
	sm.RegisterStaticReturnMenu(sm.StateUploadEmgOrder, sm.ReturnOption{
		Label: "Pedir de nuevo la foto de la orden de EMG", Value: "pedir_orden_emg",
		Target: sm.StateUploadEmgOrder, NoInput: true,
	})
}

// agentCupsReturnInput es el campo de CUPS que el agente de Néctar llena en los pasos de la orden.
// Validate lee con services.ParseAgentCups, la MISMA gramática del comando `/bot cups`, para que
// un dato que el campo acepta nunca sea uno que el comando rechaza.
func agentCupsReturnInput() sm.ReturnInput {
	return sm.ReturnInput{
		Kind:     sm.InputKindText,
		Label:    "CUPS leídos de la orden (código[:cantidad], separados por espacio)",
		Required: true,
		Command:  sm.ReturnCommandCups,
		Validate: func(_ *session.Session, value string) sm.InputRejection {
			cups := services.ParseAgentCups(value)
			if len(cups) == 0 {
				return sm.Reject(services.AgentCupsNoCodesProblem)
			}
			// Mismo tope que VALIDATE_OCR: rechazada aquí, el agente la corrige en el formulario en
			// vez de que el bot se la vuelva a escalar.
			for _, c := range cups {
				if c.Quantity > maxCupsQuantity {
					return sm.Reject(fmt.Sprintf("La cantidad máxima por CUPS es %d (%s tiene %d).", maxCupsQuantity, c.Code, c.Quantity))
				}
			}
			return sm.InputRejection{}
		},
	}
}

// agentCupsReplaceOrderInput es el campo de CUPS de los pasos donde la orden ya está agrupada
// (SEARCH_SLOTS, CHECK_SPECIAL_CUPS): lo escrito vuelve a VALIDATE_OCR y REEMPLAZA la orden entera,
// así que se borra todo lo que pertenecía a sus grupos (procedureGroupContextKeys) y el agente lo
// lee en la ayuda del campo.
func agentCupsReplaceOrderInput() sm.ReturnInput {
	in := agentCupsReturnInput()
	in.Help = "Lo escrito reemplaza la orden completa, procedimientos pendientes incluidos."
	in.Clear = slices.Clone(procedureGroupContextKeys)
	return in
}

// ocrRetryButtons builds the retry/escalation buttons for OCR errors.
// Only includes "Hablar con agente" if agents are available.
func ocrRetryButtons(birdClient *bird.Client, sess *session.Session) []sm.Button {
	buttons := []sm.Button{
		{Text: "Enviar de nuevo", Payload: "retry_photo"},
	}
	if birdClient != nil && birdClient.HasAvailableAgentsFor(sess.PhoneNumber, sess.ConversationID) {
		buttons = append(buttons, sm.Button{Text: "Hablar con agente", Payload: "escalate_agent"})
	}
	return buttons
}

// ocrFailureMessage devuelve un mensaje ACCIONABLE según la causa del fallo del OCR (§5e.2). El OCR
// reporta la causa en OCRResult.Error (imagen_borrosa / no_table_detected / formato_no_soportado); para
// esas se da una instrucción concreta. La mayoría de fallos llegan con causa vacía → mensaje genérico
// pero con consejos (buena luz, enfoque, que se vea la tabla de códigos) para subir la tasa de reintento útil.
func ocrFailureMessage(errReason string) string {
	switch errReason {
	case "imagen_borrosa":
		return "La foto se ve *borrosa* 📷. Tómala de nuevo con buena luz y enfoque, que el texto se lea claro.\n\n¿Qué deseas hacer?"
	case "no_table_detected":
		return "No encontré la *tabla de procedimientos* en la imagen. Fotografía la parte de la orden donde están los *códigos/CUPS*.\n\n¿Qué deseas hacer?"
	case "formato_no_soportado":
		return "Ese formato no puedo leerlo. Envía una *foto (JPG)* o un *PDF* de tu orden médica.\n\n¿Qué deseas hacer?"
	default:
		return "No pudimos leer los procedimientos de tu orden.\n\nConsejo: toma la foto con *buena luz*, *enfocada* y que se vea la *tabla de códigos* completa.\n\n¿Qué deseas hacer?"
	}
}

// ASK_MEDICAL_ORDER (automático) — pide foto de la orden y transiciona a UPLOAD.
// Para pacientes PARTICULAR: primero pregunta si tienen orden médica.
// Si sí → sube foto y flujo normal. Si no → escala a agente y registra en lista de espera.
func askMedicalOrderHandler(wlRepo WaitingListCreator, ocrSvc *services.OCRService, birdClient *bird.Client) sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (res *sm.StateResult, err error) {
		// El stash que NO se pudo leer debe borrarse de la BD, no solo del mapa en memoria: la sesión se
		// recarga de BD en el mensaje siguiente y la URL rota volvería, reintentándose para siempre
		// (auditoría ciclo 133: el mismo stash falló dos veces en sess:7cbdfb83 sin foto nueva en medio).
		// Como el camino de fallo sigue hacia abajo y tiene varios returns, el borrado se engancha aquí.
		clearStash := false
		defer func() {
			if clearStash && res != nil {
				res.ClearCtx = append(res.ClearCtx, sm.StashKeysToClear()...)
			}
		}()

		// Flujo "Aplicar medicamentos": reutiliza la identificación/registro de agendar, pero NO pide
		// orden — al llegar aquí desvía a la validación SANITAS + canal/escalación (único punto de
		// intercepción, así no hay que tocar cada ruteo del flujo de agendar).
		if sess.GetContext("medication_flow") == "1" {
			return sm.NewResult(sm.StateMedicationCheckSanitas), nil
		}

		// Antes de pedir la orden: si el paciente ya tiene citas ACTIVAS en lista de espera, ofrecerle
		// elegir una de ellas (retoma en la búsqueda de slots con los datos guardados) o seguir como
		// cita nueva. Si no tiene ninguna, no se pregunta nada y continúa el flujo normal.
		if wlRepo != nil && sess.GetContext("wl_offer_done") == "" {
			if entries, err := wlRepo.GetActiveByPatient(ctx, sess.GetContext("patient_id")); err == nil && len(entries) > 0 {
				return buildWaitingListSelection(entries), nil
			}
		}

		// Orden GUARDADA antes de llegar aquí (stash, §8.1 #1): usarla directamente, sin re-pedir la foto.
		// Un solo intento: el stash se limpia SIEMPRE; si el OCR falla se cae al flujo normal (pedir foto).
		if pages := sm.StashedOrderURLs(sess); len(pages) > 0 && ocrSvc != nil {
			stashRes, stashErr := processStashedOrder(ctx, sess, ocrSvc, birdClient, pages)
			if stashErr == nil && stashRes != nil && (stashRes.NextState == sm.StateValidateOCR || stashRes.NextState == sm.StateEscalateToAgent) {
				stashRes.ClearCtx = append(stashRes.ClearCtx, sm.StashKeysToClear()...)
				stashRes.WithEvent("stashed_order_used", map[string]interface{}{"pages": len(pages)})
				observability.Emit(observability.TraceSession(sess.ID), "agendar", "stashed_order_used",
					observability.EmitOpts{Phone: sess.PhoneNumber, Attrs: map[string]interface{}{"n": len(pages)}})
				return stashRes, nil
			}
			// No se pudo leer la orden guardada → limpiar (memoria + BD vía clearStash) y pedirla como
			// siempre. El borrado en memoria evita que el resto de ESTA pasada la vuelva a ver.
			clearStash = true
			for _, k := range sm.StashKeysToClear() {
				sess.SetContext(k, "")
			}
			observability.Emit(observability.TraceSession(sess.ID), "agendar", "stashed_order_failed",
				observability.EmitOpts{Phone: sess.PhoneNumber, Attrs: map[string]interface{}{"n": len(pages)}})
		}

		// Pacientes PARTICULAR: preguntar si tienen orden antes de pedir foto
		if sess.GetContext("entity_category") == "PARTICULAR" {
			// Primera entrada (auto-chain): mostrar botones y detener la cadena
			if sess.GetContext("_prompted_has_order") == "" {
				return sm.NewResult(sess.CurrentState).
					WithContext("_prompted_has_order", "1").
					WithButtons(
						"¿Cuentas con una *orden médica* para este procedimiento?",
						sm.Button{Text: "Sí, tengo orden", Payload: "has_order_yes"},
						sm.Button{Text: "No tengo orden", Payload: "has_order_no"},
					), nil
			}

			result, selected := sm.ValidateButtonResponse(sess, msg, "has_order_yes", "has_order_no")
			if result != nil {
				// Reintentos agotados: el paciente recibe el aviso de la escalación, no la pregunta otra vez.
				if result.NextState == sm.StateEscalateToAgent {
					return result, nil
				}
				result.Messages = []sm.OutboundMessage{&sm.ButtonMessage{
					Text: "¿Cuentas con una *orden médica* para este procedimiento?",
					Buttons: []sm.Button{
						{Text: "Sí, tengo orden", Payload: "has_order_yes"},
						{Text: "No tengo orden", Payload: "has_order_no"},
					},
				}}
				return result, nil
			}

			switch selected {
			case "has_order_yes":
				return sm.NewResult(sm.StateUploadMedicalOrder).
					WithClearCtx("_prompted_has_order").
					WithText("Envía una *foto clara* o *PDF* de tu orden médica.\n\nAsegúrate de que:\n- Se vean bien los procedimientos\n- La foto no esté borrosa\n- Se lea el texto").
					WithEvent("order_method_selected", map[string]interface{}{"method": "photo"}).
					WithEvent("order_photo_requested", nil), nil

			case "has_order_no":
				// Registrar en lista de espera (sin cups, pendiente de agente)
				waitingListID := ""
				if wlRepo != nil {
					age, _ := strconv.Atoi(sess.GetContext("patient_age"))
					gender := sess.GetContext("patient_gender")
					if gender == "" {
						gender = "M" // default requerido por NOT NULL
					}
					entry := &domain.WaitingListEntry{
						ID:             uuid.New().String(),
						PhoneNumber:    waitingListPhone(sess),
						PatientID:      sess.GetContext("patient_id"),
						PatientDoc:     sess.GetContext("patient_doc"),
						PatientName:    sess.GetContext("patient_name"),
						PatientAge:     age,
						PatientGender:  gender,
						PatientEntity:  sess.GetContext("patient_entity"),
						ContractCode:   sess.GetContext("patient_contract"),
						CupsCode:       "PARTICULAR",
						CupsName:       "Consulta PARTICULAR - Sin orden médica",
						ProceduresJSON: "[]",
						Espacios:       1,
						Status:         "pending_agent",
						CreatedAt:      time.Now(),
						ExpiresAt:      time.Now().AddDate(0, 0, 30),
					}
					if err := wlRepo.Create(ctx, entry); err != nil {
						// No bloquear el flujo si falla el registro
						slog.Warn("particular_no_order: failed to create wl entry",
							"phone", utils.MaskPhone(sess.PhoneNumber), "error", err)
					} else {
						waitingListID = entry.ID
					}
				}

				res := sm.NewResult(sm.StateEscalateToAgent).
					WithClearCtx("_prompted_has_order").
					WithText("Entendido. En un momento uno de nuestros asesores se comunicará contigo para brindarte la atención que necesitas.").
					WithContext(sm.CtxEscalationReason, sm.EscalationReasonParticularNoOrder).
					WithEvent("particular_no_order_escalated", nil)
				if waitingListID != "" {
					// El agente ve a qué entrada de la lista de espera quedó atado el paciente.
					res.WithContext(sm.CtxEscalationWaitingListID, waitingListID)
				}
				return res, nil
			}
		}

		// Resto de entidades: proceder directamente a subir foto
		return sm.NewResult(sm.StateUploadMedicalOrder).
			WithText("Envía una *foto clara* o *PDF* de tu orden médica.\n\nAsegúrate de que:\n- Se vean bien los procedimientos\n- La foto no esté borrosa\n- Se lea el texto").
			WithEvent("order_method_selected", map[string]interface{}{"method": "photo"}).
			WithEvent("order_photo_requested", nil), nil
	}
}

func boolTo1(b bool) string {
	if b {
		return "1"
	}
	return ""
}

// buildWaitingListSelection arma la lista para que el paciente elija una cita ya en espera o "nueva".
func buildWaitingListSelection(entries []domain.WaitingListEntry) *sm.StateResult {
	rows := make([]sm.ListRow, 0, len(entries)+1)
	for i, e := range entries {
		if i >= 9 { // límite de filas de lista en WhatsApp (dejamos 1 para "cita nueva")
			break
		}
		rows = append(rows, sm.ListRow{
			ID:          e.ID,
			Title:       truncate(e.CupsName, 24),
			Description: "En espera desde " + e.CreatedAt.Format("2006-01-02"),
		})
	}
	rows = append(rows, sm.ListRow{ID: "wl_new", Title: "➕ Es una cita nueva"})
	return sm.NewResult(sm.StateSelectWaitingList).
		WithList(
			"Tienes procedimientos en *lista de espera*. ¿Deseas agendar uno de estos ahora o es una *cita nueva*?",
			"Ver opciones",
			sm.ListSection{Title: "En lista de espera", Rows: rows},
		).
		WithEvent("waiting_list_offered", map[string]interface{}{"count": len(entries)})
}

// SELECT_WAITING_LIST (interactivo) — el paciente elige una cita ya en lista de espera (retoma la
// búsqueda de slots con los datos guardados) o "cita nueva" (sigue al flujo de subir la orden).
func selectWaitingListHandler(wlRepo WaitingListCreator) sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		choice := strings.TrimSpace(msg.Text)
		if msg.IsPostback {
			choice = msg.PostbackPayload
		}

		// Retorno sin dato del agente de Néctar: `__resume__` no es una entrada de la lista. Se le
		// vuelve a mostrar la lista vigente; si ya no hay entradas activas, sigue con la orden nueva.
		if sm.IsAgentResumeTurn(sess) && sm.IsReservedKeyword(choice) {
			if wlRepo != nil {
				if entries, err := wlRepo.GetActiveByPatient(ctx, sess.GetContext("patient_id")); err == nil && len(entries) > 0 {
					r := buildWaitingListSelection(entries)
					r.Events = nil
					return r.WithEvent("agent_resume_reprompt", map[string]interface{}{"state": sess.CurrentState}), nil
				}
			}
			return sm.NewResult(sm.StateAskMedicalOrder).
				WithContext("wl_offer_done", "1"), nil
		}

		// "Cita nueva" (o sin selección clara) → seguir al flujo normal de subir la orden.
		if choice == "" || choice == "wl_new" {
			return sm.NewResult(sm.StateAskMedicalOrder).
				WithContext("wl_offer_done", "1").
				WithEvent("waiting_list_new_chosen", nil), nil
		}

		entry, err := wlRepo.FindByID(ctx, choice)
		if err != nil || entry == nil {
			// Si no se pudo cargar, continuar con la orden nueva; el error no se propaga a la FSM.
			res := sm.NewResult(sm.StateAskMedicalOrder).
				WithContext("wl_offer_done", "1").
				WithText("No pude cargar esa cita en espera. Continuemos con tu orden médica.")
			return res, nil //nolint:nilerr // manejado re-preguntando, no se propaga
		}

		// Cargar la cita al contexto (mismo mapeo que el flujo de notificación proactiva) y retomar en
		// la búsqueda de slots. waiting_list_entry_id → createAppointmentHandler la marca 'scheduled'.
		// from_waiting_list=1 → si no hay slots, no se re-registra (ya existe).
		r := sm.NewResult(sm.StateSearchSlots).
			WithContext("cups_code", entry.CupsCode).
			WithContext("cups_name", entry.CupsName).
			WithContext("espacios", strconv.Itoa(entry.Espacios)).
			WithContext("is_contrasted", boolTo1(entry.IsContrasted)).
			WithContext("is_sedated", boolTo1(entry.IsSedated)).
			WithContext("procedure_type", entry.ProcedureType).
			WithContext("procedures_json", entry.ProceduresJSON).
			WithContext("total_procedures", "1").
			WithContext("current_procedure_idx", "0").
			WithContext("patient_contract", entry.ContractCode).
			WithContext("waiting_list_entry_id", entry.ID).
			WithContext("from_waiting_list", "1").
			WithContext("wl_offer_done", "1").
			WithText("Vamos a buscar horarios disponibles para *"+entry.CupsName+"*...").
			WithEvent("waiting_list_selected", map[string]interface{}{"entry_id": entry.ID, "cups_code": entry.CupsCode})

		if entry.PreferredDoctorDoc != "" {
			r.WithContext("preferred_doctor_doc", entry.PreferredDoctorDoc)
		}
		if entry.GfrCreatinine > 0 {
			r.WithContext("gfr_creatinine", fmt.Sprintf("%.2f", entry.GfrCreatinine)).
				WithContext("gfr_height_cm", strconv.Itoa(entry.GfrHeightCm)).
				WithContext("gfr_weight_kg", fmt.Sprintf("%.1f", entry.GfrWeightKg)).
				WithContext("gfr_disease_type", entry.GfrDiseaseType).
				WithContext("gfr_calculated", fmt.Sprintf("%.1f", entry.GfrCalculated))
		}
		if entry.IsPregnant {
			r.WithContext("is_pregnant", "1")
		}
		if entry.BabyWeightCat != "" {
			r.WithContext("baby_weight_cat", entry.BabyWeightCat)
		}
		return r, nil
	}
}

// UPLOAD_MEDICAL_ORDER (interactivo) — espera imagen, procesa OCR
func uploadMedicalOrderHandler(ocrSvc *services.OCRService, birdClient *bird.Client) sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		switch msg.MessageType {
		case "image", "document":
			mediaURL := msg.ImageURL
			if msg.MessageType == "document" {
				mediaURL = msg.DocumentURL
			}
			if mediaURL == "" {
				return sm.NewResult(sess.CurrentState).
					WithText("No pudimos obtener el archivo. Por favor envía otra foto o PDF."), nil
			}
			res, err := processOrderMedia(ctx, sess, ocrSvc, birdClient, mediaURL)
			if err != nil {
				return res, err
			}
			return capFailedOrderReads(sess, res), nil

		default:
			// (el resto del switch sigue abajo)
			return uploadNonMedia(sess, birdClient, msg)
		}
	}
}

// maxFailedOrderReads es cuántas lecturas fallidas SEGUIDAS de la orden aguanta una sesión antes de
// pasarla a un agente. Cada lectura son una o dos llamadas al LLM, y sin tope un paciente atascado
// (o un número que manda en bucle archivos sin procedimientos) recibía «Enviar de nuevo» sin fin.
const maxFailedOrderReads = 3

// ocrUnreadEscalationText es el aviso al paciente cuando la orden no se pudo leer y pasa a un agente.
const ocrUnreadEscalationText = "No pudimos leer el código (CUPS) de tu orden. Te comunico con un agente para ayudarte a agendar tu cita."

// failedOrderReads es el contador de lecturas fallidas seguidas de la sesión (CtxOCRFailedReads).
func failedOrderReads(sess *session.Session) int {
	n, _ := strconv.Atoi(sess.GetContext(sm.CtxOCRFailedReads))
	return n
}

// capFailedOrderReads cuenta la lectura fallida que devolvió processOrderMedia (las salidas que
// ofrecen «Enviar de nuevo»: error, tiempo agotado, lectura sin éxito o sin procedimientos; todas
// dejan CtxOCRFailureCause) y, a la maxFailedOrderReads-ésima, escala en vez de ofrecer otro intento.
// Toda escalación borra el contador (escalationExit): si el agente pide la foto otra vez, el paciente
// vuelve a tener sus intentos. Una lectura buena lo borra en processOrderMedia. Solo lo usa el paso de subida: la orden
// guardada es un intento único que, si falla, pide la foto como siempre.
func capFailedOrderReads(sess *session.Session, res *sm.StateResult) *sm.StateResult {
	if res == nil || res.NextState != sess.CurrentState || res.UpdateCtx[sm.CtxOCRFailureCause] == "" {
		return res
	}
	n := failedOrderReads(sess) + 1
	if n < maxFailedOrderReads {
		return res.WithContext(sm.CtxOCRFailedReads, strconv.Itoa(n))
	}
	cause := res.UpdateCtx[sm.CtxOCRFailureCause]
	observability.Emit(observability.TraceSession(sess.ID), "agendar", "ocr_failed_reads_cap",
		observability.EmitOpts{Phone: sess.PhoneNumber, Reason: cause, Attrs: map[string]interface{}{"n": n}})
	esc := sm.NewResult(sm.StateEscalateToAgent).
		WithText(ocrUnreadEscalationText).
		WithContext(sm.CtxEscalationReason, sm.EscalationReasonOCRFailedReads).
		WithContext(sm.CtxOCRFailureCause, cause).
		WithEvent("ocr_failed_reads_cap", map[string]interface{}{"reads": n, "cause": cause})
	// Los eventos de la lectura fallida (ocr_error, ocr_failed…) se conservan.
	esc.Events = append(res.Events, esc.Events...)
	return esc
}

// minBudgetForExtraPage es lo que debe quedarle al mensaje para intentar OTRA hoja del stash. El
// presupuesto de UN análisis es de 75s dentro de los 2 min del mensaje: sin esta guarda, una orden de
// varias hojas podría consumir el mensaje entero y tumbar la persistencia posterior (mismo modo de
// fallo que el bug de presupuesto OCR de 2026-07-27).
const minBudgetForExtraPage = 25 * time.Second

// budgetAllowsExtraPage indica si al mensaje le queda tiempo para un análisis más.
func budgetAllowsExtraPage(ctx context.Context) bool {
	dl, ok := ctx.Deadline()
	return !ok || time.Until(dl) > minBudgetForExtraPage
}

// processStashedOrder consume la orden GUARDADA, que puede tener varias hojas: el paciente manda una
// orden de dos o tres páginas como varias fotos seguidas antes de llegar al paso de la orden (auditoría
// ciclo 133). La primera hoja decide el siguiente paso (mismo camino de siempre) y las demás se fusionan
// con el mismo mecanismo que las páginas adicionales del paso "¿Es correcto?" (mergeCupsJSON, dedupe por
// código). Una hoja ilegible NO tumba la orden: se registra y se sigue con las que sí se leyeron.
func processStashedOrder(ctx context.Context, sess *session.Session, ocrSvc *services.OCRService, birdClient *bird.Client, pages []string) (*sm.StateResult, error) {
	res, err := processOrderMedia(ctx, sess, ocrSvc, birdClient, pages[0])
	if err != nil || res == nil || res.NextState != sm.StateValidateOCR {
		return res, err
	}

	for _, url := range pages[1:] {
		if !budgetAllowsExtraPage(ctx) {
			slog.Warn("stash_extra_page_skipped_budget", "session_id", sess.ID, "phone", utils.MaskPhone(sess.PhoneNumber))
			break
		}
		ocrRes, ocrErr := ocrSvc.AnalyzeDocument(ctx, url)
		if ocrErr != nil || ocrRes == nil || !ocrRes.Success || len(ocrRes.Cups) == 0 {
			observability.Emit(observability.TraceSession(sess.ID), "agendar", "ocr_append_failed",
				observability.EmitOpts{Phone: sess.PhoneNumber})
			continue
		}
		merged, added := mergeCupsJSON(res.UpdateCtx["ocr_cups_json"], ocrRes.Cups)
		res.WithContext("ocr_cups_json", merged)
		// Con CUPS de varias hojas no se sabe de cuál salió cada uno: sin segunda lectura del código.
		delete(res.UpdateCtx, ctxOCROrderURL)
		observability.Emit(observability.TraceSession(sess.ID), "agendar", "ocr_page_appended",
			observability.EmitOpts{Phone: sess.PhoneNumber, Attrs: map[string]interface{}{"n": added}})
	}
	return res, nil
}

// processOrderMedia corre el OCR sobre una imagen/PDF de orden médica y decide el siguiente paso.
// Extraído de uploadMedicalOrderHandler SIN cambios de lógica para poder reutilizarlo con la orden
// GUARDADA del primer mensaje (stash, §8.1 #1): mismo código para foto recién enviada o guardada.
func processOrderMedia(ctx context.Context, sess *session.Session, ocrSvc *services.OCRService, birdClient *bird.Client, mediaURL string) (*sm.StateResult, error) {
	ocrResult, err := ocrSvc.AnalyzeDocument(ctx, mediaURL)
	if err != nil {
		msg := "No pudimos procesar la imagen. ¿Qué deseas hacer?"
		eventType := "ocr_error"
		if errors.Is(err, context.DeadlineExceeded) {
			msg = "El análisis de la imagen tardó demasiado. ¿Qué deseas hacer?"
			eventType = "ocr_timeout"
		}
		observability.Emit(observability.TraceSession(sess.ID), "agendar", "ocr_failed",
			observability.EmitOpts{Phone: sess.PhoneNumber, Reason: eventType})
		return sm.NewResult(sess.CurrentState).
			WithButtons(msg, ocrRetryButtons(birdClient, sess)...). //nolint:contextcheck // el cliente de mensajería no toma ctx (patrón existente: HasAvailableAgents)
			WithContext(sm.CtxOCRFailureCause, eventType).
			WithEvent(eventType, map[string]interface{}{"error": err.Error()}), nil
	}

	if !ocrResult.Success || len(ocrResult.Cups) == 0 {
		cause := ocrResult.Error
		if cause == "" {
			cause = sm.OCRFailureNoProcedures
		}
		return sm.NewResult(sess.CurrentState).
			WithButtons(
				ocrFailureMessage(ocrResult.Error),
				ocrRetryButtons(birdClient, sess)..., //nolint:contextcheck // el cliente de mensajería no toma ctx (patrón existente: HasAvailableAgents)
			).
			WithContext(sm.CtxOCRFailureCause, cause).
			WithEvent("ocr_failed", map[string]interface{}{"error": ocrResult.Error}), nil
	}

	// GUARDA (el CÓDIGO CUPS es prioridad para agendar): un CUP sin código NO debe llegar al gate de
	// cobertura, que lo interpretaría como "sin convenio" y mandaría a particular. Los que traen el
	// nombre siguen a VALIDATE_OCR, que les asigna el código por nombre o los pasa a un agente
	// (cups_name_assign.go); si no queda ninguno con código ni nombre, se escala aquí.
	readable := make([]services.CUPSEntry, 0, len(ocrResult.Cups))
	for _, c := range ocrResult.Cups {
		if strings.TrimSpace(c.Code) != "" || strings.TrimSpace(c.Name) != "" {
			readable = append(readable, c)
		}
	}
	if len(readable) == 0 {
		observability.Emit(observability.TraceSession(sess.ID), "agendar", "ocr_no_cups_code",
			observability.EmitOpts{Phone: sess.PhoneNumber})
		return sm.NewResult(sm.StateEscalateToAgent).
			WithText(ocrUnreadEscalationText).
			WithContext(sm.CtxEscalationReason, sm.EscalationReasonOCRNoCode).
			WithClearCtx(sm.CtxOCRFailureCause, sm.CtxOCRUnreadNames).
			WithEvent("ocr_no_cups_code", map[string]interface{}{"cups_count": len(ocrResult.Cups)}), nil
	}
	ocrResult.Cups = readable

	// OCR exitoso — guardar CUPS en contexto
	cupsJSON, _ := json.Marshal(ocrResult.Cups)

	r := sm.NewResult(sm.StateValidateOCR).
		WithContext("ocr_cups_json", string(cupsJSON)).
		WithContext(ctxOCROrderURL, mediaURL). // para la segunda lectura del código en VALIDATE_OCR
		// Orden nueva: las correcciones y asignaciones de la anterior no son suyas (las anota VALIDATE_OCR).
		WithClearCtx(sm.CtxOCRFailureCause, sm.CtxOCRUnreadNames, sm.CtxOCRFailedReads, ctxCupsCorrections, ctxCupsNameAssignments).
		WithEvent("ocr_success", map[string]interface{}{"cups_count": len(ocrResult.Cups)})
	observability.Emit(observability.TraceSession(sess.ID), "agendar", "ocr_ok",
		observability.EmitOpts{Phone: sess.PhoneNumber, Attrs: map[string]interface{}{"n": len(ocrResult.Cups)}})

	// Guardar documento extraído para verificación posterior
	if ocrResult.Document != "" {
		r.WithContext("ocr_document", ocrResult.Document)
	}

	return r, nil
}

// uploadNonMedia maneja texto/postbacks en UPLOAD_MEDICAL_ORDER (extraído del default del switch,
// sin cambios de lógica).
func uploadNonMedia(sess *session.Session, birdClient *bird.Client, msg bird.InboundMessage) (*sm.StateResult, error) {
	// Texto u otro tipo de mensaje
	if msg.IsPostback {
		switch msg.PostbackPayload {
		case "retry_photo":
			return sm.NewResult(sess.CurrentState).
				WithText("Envía otra foto o PDF de tu orden médica."), nil
		case "escalate_agent":
			// Re-check agent availability (button may have been shown before agents went offline)
			if birdClient != nil && !birdClient.HasAvailableAgentsFor(sess.PhoneNumber, sess.ConversationID) {
				return sm.NewResult(sess.CurrentState).
					WithButtons(
						"En este momento no hay agentes disponibles. ¿Qué deseas hacer?",
						sm.Button{Text: "Enviar de nuevo", Payload: "retry_photo"},
					).
					WithEvent("agent_unavailable_at_escalation", nil), nil
			}
			return sm.NewResult(sm.StateEscalateToAgent).
				WithText("Te voy a comunicar con uno de nuestros agentes para que pueda ayudarte con tu orden médica.").
				WithContext(sm.CtxEscalationReason, sm.EscalationReasonOCRAskedForAgent).
				WithContext(sm.CtxPatientAskedForAgent, "1").
				WithEvent("ocr_escalate_to_agent", nil), nil
		}
	}

	return sm.RetryOrEscalate(sess, "Estoy esperando una *foto o PDF* de tu orden médica. Por favor envía el archivo."), nil
}

// maxCupsQuantity es la cantidad máxima de un CUPS que el bot agenda solo. La cantidad la extrae
// el LLM de la orden (el «(#N)» tal cual) y cada unidad suma espacios en la agenda: sin tope, una
// orden manipulada o mal leída bloquearía media jornada de un técnico. Ninguna orden real se acerca
// a 100; por encima la revisa un agente. EMG/NC conservan además su propio tope (applyFisiatriaRules).
const maxCupsQuantity = 100

// orderUnprocessableEscalationText es el aviso al paciente cuando VALIDATE_OCR pasa la orden a un
// agente porque lo leído no se puede agendar tal cual.
const orderUnprocessableEscalationText = "No pudimos procesar los procedimientos de tu orden médica. Te voy a comunicar con un agente para que pueda ayudarte."

// VALIDATE_OCR (automático) — muestra resultados del OCR con verificación de documento
func validateOCRHandler(procedureRepo repository.ProcedureRepository, cupsReader CupsCodeReader) sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, _ bird.InboundMessage) (res *sm.StateResult, _ error) {
		// La URL de la orden es solo de ESTA validación (cups_resolve.go): se borra en toda salida.
		// Las correcciones y las asignaciones por nombre se anotan, o se borran, una vez validada la lista.
		var corrections, assignments []cupsCorrection
		// unreadNames: los procedimientos sin código que no se asignaron (con duda o descartados). Quedan
		// para el agente en CtxOCRUnreadNames en toda salida, también si la orden escala por otro motivo.
		var unreadNames []string
		validated := false
		defer func() {
			if res == nil {
				return
			}
			res.WithClearCtx(ctxOCROrderURL)
			if !validated {
				return
			}
			if len(unreadNames) > 0 {
				res.WithContext(sm.CtxOCRUnreadNames, strings.Join(unreadNames, "; "))
			}
			for key, list := range map[string][]cupsCorrection{ctxCupsCorrections: corrections, ctxCupsNameAssignments: assignments} {
				if raw, merr := json.Marshal(list); merr == nil && len(list) > 0 {
					res.WithContext(key, string(raw))
				} else {
					res.WithClearCtx(key)
				}
			}
		}()

		var cups []services.CUPSEntry
		if err := json.Unmarshal([]byte(sess.GetContext("ocr_cups_json")), &cups); err != nil {
			return sm.NewResult(sm.StateEscalateToAgent).
				WithText("No pudimos procesar los datos de tu orden. Te voy a comunicar con un agente para que pueda ayudarte.").
				WithContext(sm.CtxEscalationReason, sm.EscalationReasonOCRInvalidJSON).
				WithEvent("ocr_parse_error", map[string]interface{}{"error": err.Error()}), nil
		}

		// Validar CUPS contra la BD — filtrar inactivos, enriquecer nombres.
		// Si el código tiene sufijo numérico (ej "891509-16"), busca el código base interno.
		// Un código que no está en el catálogo, o con otra especialidad, pasa por la resolución
		// (cups_resolve.go): se corrige, se deja en duda para un agente o se descarta como siempre.
		var skipped, doubts []string
		var fresh []cupsCorrection
		resolver := newCupsResolver(procedureRepo, cupsReader, sess, cups)
		trace := observability.TraceSession(sess.ID)

		// Sin código: asignación por nombre (cups_name_assign.go). Nunca llega a la cobertura un CUPS sin
		// código. Con candidatos pero sin asignación segura es duda para un agente; sin ningún candidato
		// se descarta como siempre (como el paso 4 de H5) y queda anotado en CtxOCRUnreadNames.
		var unreadNotes, discardNotes []string
		var freshAssigned []cupsCorrection
		withCode := make([]services.CUPSEntry, 0, len(cups))
		for _, cup := range cups {
			if strings.TrimSpace(cup.Code) != "" {
				withCode = append(withCode, cup)
				continue
			}
			name := strings.TrimSpace(cup.Name)
			if name == "" {
				continue
			}
			r := resolver.assignByName(ctx, cup)
			if r.Code == "" {
				unreadNames = append(unreadNames, name)
				if len(r.Candidates) == 0 {
					skipped = append(skipped, name)
					discardNotes = append(discardNotes, cupsNameDoubtNote(name, nil))
				} else {
					unreadNotes = append(unreadNotes, cupsNameDoubtNote(name, r.Candidates))
				}
				continue
			}
			observability.Emit(trace, "agendar", "cups_asignado_nombre", observability.EmitOpts{
				Phone: sess.PhoneNumber, Reason: r.Via, Attrs: map[string]interface{}{"usado": r.Code},
			})
			freshAssigned = append(freshAssigned, cupsCorrection{Used: r.Code, Via: r.Via, Name: name})
			cup.Code, cup.OrderName = r.Code, name
			withCode = append(withCode, cup)
		}
		cups = withCode

		valid := cups[:0]
		for _, cup := range cups {
			if cup.Code != "" {
				proc, code, exists := findActiveProcedure(ctx, procedureRepo, cup.Code)

				// Un código real del catálogo que no está activo: Neuro no lo agenda. Se descarta como
				// siempre, sin resolución (sus hermanos a un dígito son otros estudios reales).
				if proc == nil && exists {
					skipped = append(skipped, code)
					continue
				}

				// Validación cruzada de especialidad: si el nombre que leyó el OCR menciona
				// una especialidad claramente distinta a la del catálogo, es un error de dígito
				// del OCR (ej: 890382 Otorrino leído como 890364 Fisiatría).
				mismatch := proc != nil && ocrSpecialtyMismatch(cup.Name, proc.Name)
				if mismatch {
					slog.Warn("validate_ocr_specialty_mismatch",
						"cups_code", code,
						"ocr_name", cup.Name,
						"catalog_name", proc.Name,
					)
				}

				if proc == nil || mismatch {
					r := resolver.resolve(ctx, cup)
					switch {
					case r.proc != nil:
						observability.Emit(trace, "agendar", "cups_corregido", observability.EmitOpts{
							Phone: sess.PhoneNumber, Reason: r.via,
							Attrs: map[string]interface{}{"leido": cup.Code, "usado": r.proc.Code},
						})
						fresh = append(fresh, cupsCorrection{Read: cup.Code, Used: r.proc.Code, Via: r.via, Name: cup.Name})
						proc, code = r.proc, r.proc.Code
					case r.doubt:
						codes := make([]string, 0, len(r.candidates))
						for _, c := range r.candidates {
							codes = append(codes, c.Code)
						}
						observability.Emit(trace, "agendar", "cups_duda", observability.EmitOpts{
							Phone: sess.PhoneNumber, Attrs: map[string]interface{}{"leido": cup.Code, "candidatos": codes},
						})
						doubts = append(doubts, cupsDoubtNote(cup, r.candidates))
						continue
					default:
						skipped = append(skipped, code)
						continue
					}
				} else if cup.OrderName == "" && services.CupsNamesConflict(cup.Name, proc.Name) {
					// Guarda: el nombre leído CONTRADICE el del catálogo de su código (SIMPLE frente a
					// CONTRASTE, primera vez frente a control, lado, región…). Solo una contradicción
					// explícita: un nombre abreviado o que no dice nada no escala.
					observability.Emit(trace, "agendar", "cups_duda", observability.EmitOpts{
						Phone: sess.PhoneNumber, Reason: "contradiccion",
						Attrs: map[string]interface{}{"leido": cup.Code},
					})
					doubts = append(doubts, cupsConflictNote(cup, proc.Name))
					continue
				}

				cup.Code = code
				cup.Name = proc.Name
			}
			valid = append(valid, cup)
		}
		cups = valid
		corrections = keptCorrections(sess, ctxCupsCorrections, fresh, cups)
		assignments = keptCorrections(sess, ctxCupsNameAssignments, freshAssigned, cups)
		validated = true

		// Bloqueos SANITAS: por regla de negocio el bot NO agenda bloqueos (053101–053115, 048101) a
		// pacientes SANITAS (contratos 4/5/6/7). Si la orden trae un bloqueo —aunque venga con otros
		// procedimientos agendables— TODA la orden se deriva a un agente para gestión manual.
		if services.IsSanitasContract(sess.GetContext("patient_contract")) {
			for _, cup := range cups {
				if services.IsBloqueoCups(cup.Code) {
					observability.Emit(observability.TraceSession(sess.ID), "agendar", "bloqueo_sanitas_escalated",
						observability.EmitOpts{
							Phone:  sess.PhoneNumber,
							Reason: "bloqueo_sanitas",
							Attrs:  map[string]interface{}{"cups_code": cup.Code},
						})
					return sm.NewResult(sm.StateEscalateToAgent).
						WithText("Tu orden incluye un *bloqueo*, que se agenda directamente con un asesor. Te comunico con un agente para gestionar tu cita.").
						WithContext(sm.CtxEscalationReason, sm.EscalationReasonSanitasBlock).
						WithEvent("bloqueo_sanitas_escalated", map[string]interface{}{"cups_code": cup.Code}), nil
				}
			}
		}

		// Tope de cantidad (B-10): VALIDATE_OCR es el embudo de toda orden leída (foto, orden guardada,
		// páginas añadidas, orden de EMG, comandos del agente), así que se mira aquí, antes del
		// resumen y de agendar. No se recorta: el agente ve qué CUPS y qué cantidad trajo la orden.
		var overQuantity []string
		for _, cup := range cups {
			if cup.Quantity > maxCupsQuantity {
				overQuantity = append(overQuantity, fmt.Sprintf("%s (cantidad %d)", cup.Code, cup.Quantity))
			}
		}
		if len(overQuantity) > 0 {
			observability.Emit(observability.TraceSession(sess.ID), "agendar", "cups_quantity_out_of_range",
				observability.EmitOpts{Phone: sess.PhoneNumber, Attrs: map[string]interface{}{"cups": overQuantity}})
			return sm.NewResult(sm.StateEscalateToAgent).
				WithText(orderUnprocessableEscalationText).
				WithContext(sm.CtxEscalationReason, sm.EscalationReasonQuantityOutOfRange).
				WithContext("escalation_note", fmt.Sprintf(
					"La orden trae una cantidad mayor que %d, que el bot no agenda: %s. Verifica la orden y devuelve los CUPS con la cantidad correcta.",
					maxCupsQuantity, strings.Join(overQuantity, ", "))).
				WithEvent("cups_quantity_out_of_range", map[string]interface{}{"cups": overQuantity}), nil
		}

		// Procedimientos sin código con candidatos que no se pudieron asignar: un agente los lee de la
		// orden. El nombre es lo único que tiene de ellos; la nota trae además los candidatos y las otras
		// dudas. Si la orden se quedó sin ningún CUPS por los descartados, escala como siempre la orden
		// sin código.
		if len(unreadNotes) > 0 || len(cups) == 0 && len(unreadNames) > 0 {
			observability.Emit(trace, "agendar", "ocr_no_cups_code",
				observability.EmitOpts{Phone: sess.PhoneNumber, Attrs: map[string]interface{}{"n": len(unreadNames)}})
			return sm.NewResult(sm.StateEscalateToAgent).
				WithText(ocrUnreadEscalationText).
				WithContext(sm.CtxEscalationReason, sm.EscalationReasonOCRNoCode).
				WithContext("escalation_note", strings.Join(slices.Concat(unreadNotes, discardNotes, doubts), " ")).
				WithEvent("ocr_no_cups_code", map[string]interface{}{"unassigned": len(unreadNames)}), nil
		}

		// Duda de lectura (cups_resolve.go): un agente revisa la orden con lo leído y los candidatos.
		if len(doubts) > 0 {
			return sm.NewResult(sm.StateEscalateToAgent).
				WithText(orderUnprocessableEscalationText).
				WithContext(sm.CtxEscalationReason, sm.EscalationReasonCupsPossibleMisread).
				WithContext("escalation_note", strings.Join(doubts, " ")).
				WithEvent("cups_possible_misread", map[string]interface{}{"doubts": len(doubts)}), nil
		}

		if len(cups) == 0 {
			observability.Emit(observability.TraceSession(sess.ID), "agendar", "cups_none",
				observability.EmitOpts{Phone: sess.PhoneNumber})
			return sm.NewResult(sm.StateEscalateToAgent).
				WithText(orderUnprocessableEscalationText).
				WithContext(sm.CtxEscalationReason, sm.EscalationReasonNoValidCups).
				WithEvent("ocr_no_valid_cups", map[string]interface{}{"skipped": skipped}), nil
		}

		// Re-serializar con datos enriquecidos
		cupsJSON, _ := json.Marshal(cups)

		// ocr_skipped_count acompaña a ocr_cups_json (lo leído ya validado): es de la ORDEN, no de un
		// grupo, y el retorno sin dato del agente lo necesita para repetir la misma nota.
		return sm.NewResult(sm.StateConfirmOCRResult).
			WithContext("ocr_cups_json", string(cupsJSON)).
			WithContext("ocr_skipped_count", strconv.Itoa(len(skipped))).
			WithButtons(
				ocrSummaryText(cups, len(skipped)),
				sm.Button{Text: "Sí, correcto", Payload: "ocr_correct"},
				sm.Button{Text: "No, corregir", Payload: "ocr_incorrect"},
			), nil
	}
}

// ocrSummaryText es la pregunta de CONFIRM_OCR_RESULT: los procedimientos leídos de la orden
// (con su cantidad) y cuántos se omitieron por no estar disponibles.
func ocrSummaryText(cups []services.CUPSEntry, skipped int) string {
	summary := "Detectamos los siguientes procedimientos en tu orden:\n\n"
	for i, cup := range cups {
		summary += fmt.Sprintf("%d. *%s*", i+1, cup.Name)
		if cup.OrderName != "" {
			// Asignado por nombre: el paciente ve también cómo lo decía su orden.
			summary += fmt.Sprintf(" (en tu orden: «%s»)", cup.OrderName)
		}
		qty := cup.Quantity
		if qty < 1 {
			qty = 1
		}
		if qty > 1 {
			summary += fmt.Sprintf(" — Cantidad: *%d*", qty)
		}
		summary += "\n"
	}

	if skipped > 0 {
		summary += fmt.Sprintf("\n⚠️ %d procedimiento(s) de tu orden no están disponibles y fueron omitidos.\n", skipped)
	}

	return summary + "\n¿Es correcto?"
}

// CONFIRM_OCR_RESULT (interactivo) — usuario confirma o corrige
func confirmOCRResultHandler(procedureRepo repository.ProcedureRepository, birdClient *bird.Client) sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		result, selected := sm.ValidateButtonResponse(sess, msg, "ocr_correct", "ocr_incorrect")
		if result != nil {
			question := "¿Es correcto?"
			// En el retorno sin dato del agente, «¿Es correcto?» solo no dice qué: se repite lo leído.
			if sm.IsAgentResumeTurn(sess) {
				var cups []services.CUPSEntry
				if err := json.Unmarshal([]byte(sess.GetContext("ocr_cups_json")), &cups); err == nil && len(cups) > 0 {
					skipped, _ := strconv.Atoi(sess.GetContext("ocr_skipped_count"))
					question = ocrSummaryText(cups, skipped)
				}
			}
			result.Messages = []sm.OutboundMessage{&sm.ButtonMessage{
				Text: question,
				Buttons: []sm.Button{
					{Text: "Sí, correcto", Payload: "ocr_correct"},
					{Text: "No, corregir", Payload: "ocr_incorrect"},
				},
			}}
			return result, nil
		}

		switch selected {
		case "ocr_correct":
			var cups []services.CUPSEntry
			if err := json.Unmarshal([]byte(sess.GetContext("ocr_cups_json")), &cups); err != nil {
				return sm.NewResult(sm.StateEscalateToAgent).
					WithText("No pudimos procesar tu orden. Te voy a comunicar con un agente para que pueda ayudarte.").
					WithContext(sm.CtxEscalationReason, sm.EscalationReasonOCRInvalidJSON).
					WithEvent("ocr_parse_error", map[string]interface{}{"error": err.Error()}), nil
			}

			// Consolidación EMG/NC (Fisiatría): si la orden trae SOLO dependientes/NC (sin EMG), no se
			// agenda una cita independiente — se intenta consolidar con la cita EMG del paciente o pedir
			// la orden de la EMG (docs/DISENO-CONSOLIDACION-EMG-CITAS.md).
			if services.IsFisiatriaDependentOnly(cups) {
				return sm.NewResult(sm.StateCheckEmgConsolidation).
					WithClearCtx(sm.CtxOCRFailedReads).
					WithEvent("emg_dependent_only_detected", map[string]interface{}{"cups": len(cups)}), nil
			}

			// Agrupar por servicio usando reglas institucionales desde BD
			// (Fisiatría EMG/NC + Resonancia magnética con reglas de espacios y combinaciones)
			groups, err := services.GroupByServiceFromDB(ctx, cups, procedureRepo)
			if err != nil || len(groups) == 0 {
				// Fallback: cada CUPS es un grupo individual
				groups = make([]services.CUPSGroup, len(cups))
				for i, c := range cups {
					groups[i] = services.CUPSGroup{
						ServiceType: "General",
						Cups:        []services.CUPSEntry{c},
						Espacios:    c.Quantity,
					}
				}
			}

			// Separar grupos con múltiples CUPS en grupos individuales.
			// Excepciones (se mantienen juntos en una sola cita):
			//   - Fisiatría: EMG + NC van en la misma agenda
			//   - Resonancia: combinaciones (ej. abdomen+pelvis) van en la misma cita
			//   - Radiografía: todas las Rx van en una sola cita
			//   - Tomografía: todos los TAC van en una sola cita
			//   - Ecografía: todas las ecografías van en una sola cita
			//   - Neurología: ya separado por applyNeurologiaRules
			var splitGroups []services.CUPSGroup
			for _, g := range groups {
				isFisiatria := strings.EqualFold(g.ServiceType, "Fisiatria") || strings.EqualFold(g.ServiceType, "Fisiatría")
				isResonancia := strings.EqualFold(g.ServiceType, "Resonancia")
				isRadiografia := strings.EqualFold(g.ServiceType, "Radiografia") || strings.EqualFold(g.ServiceType, "Radiografía")
				isTomografia := strings.EqualFold(g.ServiceType, "Tomografia") || strings.EqualFold(g.ServiceType, "Tomografía")
				isEcografia := strings.EqualFold(g.ServiceType, "Ecografia") || strings.EqualFold(g.ServiceType, "Ecografía")
				isNeurologia := strings.EqualFold(g.ServiceType, "Neurologia") || strings.EqualFold(g.ServiceType, "Neurología")
				if isFisiatria || isResonancia || isRadiografia || isTomografia || isEcografia || isNeurologia || len(g.Cups) <= 1 {
					splitGroups = append(splitGroups, g)
					continue
				}
				// Grupos de otros servicios con múltiples CUPS → una cita por CUPS
				for _, c := range g.Cups {
					espacios := c.Quantity
					if espacios < 1 {
						espacios = 1
					}
					splitGroups = append(splitGroups, services.CUPSGroup{
						ServiceType: g.ServiceType,
						Cups:        []services.CUPSEntry{c},
						Espacios:    espacios,
					})
				}
			}
			groups = splitGroups

			// Guard: if all groups ended up empty after split, escalate
			if len(groups) == 0 {
				return sm.NewResult(sm.StateEscalateToAgent).
					WithText("No pudimos identificar procedimientos válidos en tu orden. Te comunicaremos con un agente.").
					WithContext(sm.CtxEscalationReason, sm.EscalationReasonEmptyGrouping).
					WithEvent("ocr_no_valid_cups", nil), nil
			}

			groupsJSON, _ := json.Marshal(groups)

			// La orden quedó confirmada: los añadidos que fallaron no cuentan para la siguiente.
			r := sm.NewResult(sm.StateCheckSpecialCups).
				WithClearCtx(sm.CtxOCRFailedReads).
				WithContext("procedures_json", string(groupsJSON)).
				WithContext("total_procedures", fmt.Sprintf("%d", len(groups))).
				WithContext("current_procedure_idx", "0")

			if len(groups) > 1 {
				summaryText := fmt.Sprintf("Tu orden tiene *%d grupos de procedimientos*:\n\n", len(groups))
				for i, g := range groups {
					cupNames := make([]string, len(g.Cups))
					for j, c := range g.Cups {
						cupNames[j] = c.Name
					}
					summaryText += fmt.Sprintf("%d. %s (%s)\n", i+1, g.ServiceType, strings.Join(cupNames, ", "))
				}
				summaryText += "\nProcesaremos cada grupo por separado."
				r.WithText(summaryText)
			}

			// Cargar primer grupo como CUPS actual
			firstGroup := groups[0]
			if len(firstGroup.Cups) == 0 {
				return sm.NewResult(sm.StateEscalateToAgent).
					WithText("No pudimos identificar procedimientos válidos en tu orden. Te comunicaremos con un agente.").
					WithContext(sm.CtxEscalationReason, sm.EscalationReasonEmptyGrouping).
					WithEvent("ocr_empty_cups_group", nil), nil
			}

			// Para grupos con múltiples CUPS (Fisiatría, Resonancia), guardar códigos alternativos
			// para que la búsqueda de slots pueda probar con cualquiera del grupo.
			cupsForSearch := firstGroup.Cups[0]
			if len(firstGroup.Cups) > 1 {
				alternativeCodes := make([]string, 0, len(firstGroup.Cups)-1)
				for i, c := range firstGroup.Cups {
					if i == 0 {
						continue // El primero ya está en cups_code
					}
					alternativeCodes = append(alternativeCodes, c.Code)
				}
				if len(alternativeCodes) > 0 {
					r.WithContext("alternative_cups_codes", strings.Join(alternativeCodes, ","))
				}
			}

			r.WithContext("cups_code", cupsForSearch.Code).
				WithContext("cups_name", cupsForSearch.Name).
				WithContext("espacios", fmt.Sprintf("%d", firstGroup.Espacios))

			// Propagar is_sedated y is_contrasted del primer grupo si algún CUPS lo tiene
			for _, c := range firstGroup.Cups {
				if c.IsSedated {
					r.WithContext("ocr_is_sedated", "1")
					break
				}
			}
			for _, c := range firstGroup.Cups {
				if c.IsContrasted {
					r.WithContext("ocr_is_contrasted", "1")
					break
				}
			}

			return r.WithEvent("ocr_validated", map[string]interface{}{"groups": len(groups)}), nil

		case "ocr_incorrect":
			return sm.NewResult(sm.StateUploadMedicalOrder).
				WithButtons(
					"Entendido. ¿Qué deseas hacer?",
					ocrRetryButtons(birdClient, sess)..., //nolint:contextcheck // el cliente de mensajería no toma ctx (patrón existente: HasAvailableAgents)
				).
				WithClearCtx("ocr_cups_json", "ocr_skipped_count").
				WithEvent("ocr_rejected", nil), nil
		}

		return nil, fmt.Errorf("unreachable: selected=%s", selected)
	}
}

// OCR_FAILED (automático) — error en OCR, redirige
func ocrFailedHandler(birdClient *bird.Client) sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		return sm.NewResult(sm.StateUploadMedicalOrder).
			WithButtons(
				"No pudimos procesar tu orden médica. ¿Qué deseas hacer?",
				ocrRetryButtons(birdClient, sess)..., //nolint:contextcheck // el cliente de mensajería no toma ctx (patrón existente: HasAvailableAgents)
			).
			WithEvent("ocr_failed_redirect", nil), nil
	}
}

// manualCupsQuestion es la pregunta de ASK_MANUAL_CUPS que repite el retorno sin dato del agente
// de Néctar («Buscar de nuevo»). Los pasos que llevan aquí la hacen con su propio texto.
const manualCupsQuestion = "Escribe el *nombre del procedimiento* que necesitas.\n\nEjemplo: \"Electromiografía\", \"Resonancia\", \"Potenciales evocados\""

// ASK_MANUAL_CUPS (interactivo) — usuario escribe nombre del procedimiento
func askManualCupsHandler(procedureRepo repository.ProcedureRepository) sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		input := strings.TrimSpace(msg.Text)

		// El retorno sin dato del agente de Néctar (`__resume__`, p. ej. «Buscar de nuevo») no es un
		// nombre de procedimiento: se trata como entrada vacía y el paso re-pregunta sin buscarlo.
		if sm.IsAgentResumeTurn(sess) && sm.IsReservedKeyword(input) {
			input = ""
		}

		if len(input) < 3 {
			retryResult := sm.ValidateWithRetry(sess, input, func(s string) bool { return len(s) >= 3 },
				sm.ResumeText(sess, "Por favor escribe al menos 3 caracteres del nombre del procedimiento.", manualCupsQuestion))
			return retryResult, nil
		}

		// Buscar en catálogo de CUPS
		procs, err := procedureRepo.SearchByName(ctx, input)
		if err != nil {
			return sm.NewResult(sess.CurrentState).
				WithText("No pudimos buscar procedimientos en este momento. Por favor intenta de nuevo.").
				WithEvent("procedure_search_error", map[string]interface{}{"error": err.Error()}), nil
		}

		if len(procs) == 0 {
			return sm.NewResult(sess.CurrentState).
				WithText("No encontramos procedimientos con ese nombre. Intenta con otro término.\n\nEjemplo: \"Electromiografía\", \"Resonancia\", \"Potenciales evocados\"").
				WithEvent("procedure_not_found", map[string]interface{}{"query": input}), nil
		}

		if len(procs) == 1 {
			// Un solo resultado → auto-seleccionar
			proc := procs[0]
			espacios := proc.RequiredSpaces
			if espacios < 1 {
				espacios = 1
			}

			// Construir procedures_json para evitar contexto stale de sesiones previas
			singleGroup := services.CUPSGroup{
				ServiceType: "General",
				Cups:        []services.CUPSEntry{{Code: proc.Code, Name: proc.Name, Quantity: 1}},
				Espacios:    espacios,
			}
			groupsJSON, _ := json.Marshal([]services.CUPSGroup{singleGroup})

			return sm.NewResult(sm.StateCheckSpecialCups).
				WithContext("cups_code", proc.Code).
				WithContext("cups_name", proc.Name).
				WithContext("espacios", fmt.Sprintf("%d", espacios)).
				WithContext("total_procedures", "1").
				WithContext("current_procedure_idx", "0").
				WithContext("procedures_json", string(groupsJSON)).
				WithClearCtx("ocr_cups_json", "ocr_skipped_count").
				WithText(fmt.Sprintf("Procedimiento seleccionado: *%s*", proc.Name)).
				WithEvent("manual_cups_selected", map[string]interface{}{
					"code": proc.Code,
					"name": proc.Name,
				}), nil
		}

		// Múltiples resultados → mostrar lista
		procsJSON, _ := json.Marshal(procs)

		shown := make([]searchedProcedure, len(procs))
		for i, p := range procs {
			shown[i] = searchedProcedure{ID: p.ID, Code: p.Code, Name: p.Name, ServiceName: p.ServiceName, RequiredSpaces: p.RequiredSpaces}
		}

		return withProcedureList(sm.NewResult(sm.StateSelectProcedure), shown).
			WithContext("search_procedures_json", string(procsJSON)).
			WithEvent("procedure_search_results", map[string]interface{}{"count": len(procs)}), nil
	}
}

// withProcedureList le pone a r la lista de SELECT_PROCEDURE con esos procedimientos. La arman la
// búsqueda y el retorno sin dato del agente, que la vuelve a mostrar.
func withProcedureList(r *sm.StateResult, procs []searchedProcedure) *sm.StateResult {
	rows := make([]sm.ListRow, len(procs))
	for i, p := range procs {
		desc := p.ServiceName
		if desc == "" {
			desc = p.Code
		}
		rows[i] = sm.ListRow{
			ID:          fmt.Sprintf("%d", p.ID),
			Title:       truncate(p.Name, 24),
			Description: truncate(desc, 72),
		}
	}
	return r.WithList(
		fmt.Sprintf("Encontramos *%d procedimientos*.\nSelecciona el correcto:", len(rows)),
		"Ver procedimientos",
		sm.ListSection{Title: "Procedimientos", Rows: rows},
	)
}

// SELECT_PROCEDURE (interactivo) — selección de procedimiento de la lista
func selectProcedureHandler() sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		if !msg.IsPostback {
			// Retorno sin dato del agente de Néctar: se vuelve a mostrar la lista que vio el paciente.
			if sm.IsAgentResumeTurn(sess) {
				if procs, err := searchedProcedures(sess); err == nil && len(procs) > 0 {
					return withProcedureList(sm.NewResult(sess.CurrentState), procs).
						WithEvent("agent_resume_reprompt", map[string]interface{}{"state": sess.CurrentState}), nil
				}
			}
			result := sm.RetryOrEscalate(sess, "Por favor selecciona un procedimiento de la lista, o escribe otro nombre para buscar de nuevo.")
			if result.NextState == sm.StateEscalateToAgent {
				return result, nil
			}
			return sm.NewResult(sm.StateAskManualCups).
				WithText("Por favor selecciona un procedimiento de la lista, o escribe otro nombre para buscar de nuevo."), nil
		}

		selectedID := msg.PostbackPayload

		// Buscar el procedimiento seleccionado
		procs, err := searchedProcedures(sess)
		if err != nil {
			return sm.NewResult(sm.StateAskManualCups).
				WithText("No pudimos cargar los procedimientos. Por favor escribe el nombre de nuevo.").
				WithClearCtx("search_procedures_json"), nil
		}

		var selected *searchedProcedure
		for i, p := range procs {
			if fmt.Sprintf("%d", p.ID) == selectedID {
				selected = &procs[i]
				break
			}
		}

		if selected == nil {
			result := sm.RetryOrEscalate(sess, "Procedimiento no encontrado. Escribe el nombre de nuevo.")
			if result.NextState == sm.StateEscalateToAgent {
				return result, nil
			}
			return sm.NewResult(sm.StateAskManualCups).
				WithText("Procedimiento no encontrado. Escribe el nombre de nuevo.").
				WithClearCtx("search_procedures_json"), nil
		}

		espacios := selected.RequiredSpaces
		if espacios < 1 {
			espacios = 1
		}

		// Construir procedures_json para evitar contexto stale de sesiones previas
		singleGroup := services.CUPSGroup{
			ServiceType: "General",
			Cups:        []services.CUPSEntry{{Code: selected.Code, Name: selected.Name, Quantity: 1}},
			Espacios:    espacios,
		}
		groupsJSON, _ := json.Marshal([]services.CUPSGroup{singleGroup})

		return sm.NewResult(sm.StateCheckSpecialCups).
			WithContext("cups_code", selected.Code).
			WithContext("cups_name", selected.Name).
			WithContext("espacios", fmt.Sprintf("%d", espacios)).
			WithContext("total_procedures", "1").
			WithContext("current_procedure_idx", "0").
			WithContext("procedures_json", string(groupsJSON)).
			WithClearCtx("search_procedures_json", "ocr_cups_json", "ocr_skipped_count").
			WithText(fmt.Sprintf("Procedimiento seleccionado: *%s*", selected.Name)).
			WithEvent("manual_cups_selected", map[string]interface{}{
				"code": selected.Code,
				"name": selected.Name,
			}), nil
	}
}

// searchedProcedure es un resultado de la búsqueda manual tal como se guarda en
// search_procedures_json.
type searchedProcedure struct {
	ID             int    `json:"ID"`
	Code           string `json:"Code"`
	Name           string `json:"Name"`
	ServiceName    string `json:"ServiceName"`
	RequiredSpaces int    `json:"RequiredSpaces"`
}

// searchedProcedures lee los resultados de la búsqueda que se le mostraron al paciente.
func searchedProcedures(sess *session.Session) ([]searchedProcedure, error) {
	var procs []searchedProcedure
	err := json.Unmarshal([]byte(sess.GetContext("search_procedures_json")), &procs)
	return procs, err
}

// procedureReturnOptions son los procedimientos de la lista que vio el paciente, más buscar de
// nuevo (la salida cuando la lista no está). El Data es el ID de la fila, que
// selectProcedureHandler solo acepta como postback (llega así, Tarea 5); un ID que ya no está en la
// sesión deja el token desconocido.
func procedureReturnOptions(sess *session.Session) []sm.ReturnOption {
	searchAgain := sm.ReturnOption{
		Label: "Buscar de nuevo", Value: "buscar_procedimiento", Target: sm.StateAskManualCups, NoInput: true,
	}
	if sess == nil {
		return []sm.ReturnOption{searchAgain}
	}
	procs, err := searchedProcedures(sess)
	if err != nil {
		return []sm.ReturnOption{searchAgain}
	}
	options := make([]sm.ReturnOption, 0, len(procs)+1)
	for _, p := range procs {
		id := strconv.Itoa(p.ID)
		options = append(options, sm.ReturnOption{Label: p.Code + " — " + p.Name, Value: "proc_" + id, Data: id})
	}
	return append(options, searchAgain)
}

// ocrSpecialtyMismatch detecta cuando el nombre leído por el OCR indica una especialidad médica
// claramente distinta a la que tiene el catálogo para el código encontrado. Esto ocurre cuando el
// OCR confunde dígitos similares (ej: 890382 Otorrinolaringología → 890364 Fisiatría). Si hay
// discordancia, el CUPS se descarta para que el flujo escale al agente en lugar de agendar el
// procedimiento equivocado.
//
// Solo detecta especialidades ajenas a la clínica (neurología, fisiatría, imágenes) — no genera
// falsos positivos en procedimientos propios aunque tengan palabras genéricas compartidas.
func ocrSpecialtyMismatch(ocrName, catalogName string) bool {
	ocr := strings.ToUpper(ocrName)
	cat := strings.ToUpper(catalogName)

	// Especialidades externas: si el OCR las menciona pero el catálogo NO → error de dígito.
	externalKeywords := []string{
		"OTORRINOLARING", "OTORRIN",
		"CARDIOLOG",
		"GASTROENTEROLOG", "GASTRO",
		"DERMATOLOG",
		"OFTALMOLOG",
		"UROLOG",
		"GINECOLOG", "OBSTETRIC",
		"ORTOPED", "TRAUMATOLOG",
		"PSIQUIAT",
		"ENDOCRINOLOG",
		"ONCOLOG",
		"REUMATOLOG",
		"ANESTESIOL",
		"NEUMOLOG",
		"INFECTOLOG",
		"HEMATOLOG",
	}
	for _, kw := range externalKeywords {
		if strings.Contains(ocr, kw) && !strings.Contains(cat, kw) {
			return true
		}
	}

	// Términos de sub-procedimiento críticos: si el OCR los menciona pero el catálogo NO,
	// el código CUPS fue probablemente mal leído (ej: 881436 translucencia → 881431 transabdominal).
	// Se chequea en ambas direcciones para cubrir OCR correcto con código erróneo.
	specificKeywords := []string{
		"TRASLUCENCIA", "TRANSLUCENCIA", // ecografía obstétrica semana 11-14
		"DETALLE ANATOMICO", // ecografía semana 22-24
	}
	for _, kw := range specificKeywords {
		ocrHas := strings.Contains(ocr, kw)
		catHas := strings.Contains(cat, kw)
		if ocrHas != catHas {
			return true
		}
	}
	return false
}
