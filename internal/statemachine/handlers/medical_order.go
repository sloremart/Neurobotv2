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
	m.Register(sm.StateValidateOCR, validateOCRHandler(procedureRepo))
	m.Register(sm.StateConfirmOCRResult, confirmOCRResultHandler(procedureRepo, birdClient))
	m.Register(sm.StateOCRFailed, ocrFailedHandler(birdClient))
	m.Register(sm.StateAskManualCups, askManualCupsHandler(procedureRepo))
	m.Register(sm.StateSelectProcedure, selectProcedureHandler())

	// Consolidación EMG/NC entre órdenes separadas.
	m.Register(sm.StateCheckEmgConsolidation, checkEmgConsolidationHandler(apptSvc))
	m.Register(sm.StateConfirmConsolidate, confirmConsolidateHandler(apptSvc))
	m.Register(sm.StateAskEmgOrder, askEmgOrderHandler())
	m.Register(sm.StateUploadEmgOrder, uploadEmgOrderHandler(ocrSvc, birdClient))
}

// ocrRetryButtons builds the retry/escalation buttons for OCR errors.
// Only includes "Hablar con agente" if agents are available.
func ocrRetryButtons(birdClient *bird.Client) []sm.Button {
	buttons := []sm.Button{
		{Text: "Enviar de nuevo", Payload: "retry_photo"},
	}
	if birdClient != nil && birdClient.HasAvailableAgents() {
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
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		// Antes de pedir la orden: si el paciente ya tiene citas ACTIVAS en lista de espera, ofrecerle
		// elegir una de ellas (retoma en la búsqueda de slots con los datos guardados) o seguir como
		// cita nueva. Si no tiene ninguna, no se pregunta nada y continúa el flujo normal.
		if wlRepo != nil && sess.GetContext("wl_offer_done") == "" {
			if entries, err := wlRepo.GetActiveByPatient(ctx, sess.GetContext("patient_id")); err == nil && len(entries) > 0 {
				return buildWaitingListSelection(entries), nil
			}
		}

		// Orden GUARDADA del primer mensaje (stash, §8.1 #1): usarla directamente, sin re-pedir la foto.
		// Un solo intento: el stash se limpia SIEMPRE; si el OCR falla se cae al flujo normal (pedir foto).
		if u := sess.GetContext("stashed_order_url"); u != "" && ocrSvc != nil {
			res, err := processOrderMedia(ctx, sess, ocrSvc, birdClient, u)
			if err == nil && res != nil && (res.NextState == sm.StateValidateOCR || res.NextState == sm.StateEscalateToAgent) {
				res.ClearCtx = append(res.ClearCtx, "stashed_order_url")
				res.WithEvent("stashed_order_used", nil)
				observability.Emit(observability.TraceSession(sess.ID), "agendar", "stashed_order_used",
					observability.EmitOpts{Phone: sess.PhoneNumber})
				return res, nil
			}
			// No se pudo leer la orden guardada → limpiar y pedirla como siempre (flujo actual intacto).
			sess.SetContext("stashed_order_url", "")
			observability.Emit(observability.TraceSession(sess.ID), "agendar", "stashed_order_failed",
				observability.EmitOpts{Phone: sess.PhoneNumber})
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
				if wlRepo != nil {
					age, _ := strconv.Atoi(sess.GetContext("patient_age"))
					gender := sess.GetContext("patient_gender")
					if gender == "" {
						gender = "M" // default requerido por NOT NULL
					}
					entry := &domain.WaitingListEntry{
						ID:             uuid.New().String(),
						PhoneNumber:    sess.PhoneNumber,
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
					}
				}

				return sm.NewResult(sm.StateEscalateToAgent).
					WithClearCtx("_prompted_has_order").
					WithText("Entendido. En un momento uno de nuestros asesores se comunicará contigo para brindarte la atención que necesitas.").
					WithEvent("particular_no_order_escalated", nil), nil
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
	return func(ctx context.Context, _ *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		choice := strings.TrimSpace(msg.Text)
		if msg.IsPostback {
			choice = msg.PostbackPayload
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
			return processOrderMedia(ctx, sess, ocrSvc, birdClient, mediaURL)

		default:
			// (el resto del switch sigue abajo)
			return uploadNonMedia(sess, birdClient, msg)
		}
	}
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
			WithButtons(msg, ocrRetryButtons(birdClient)...).
			WithEvent(eventType, map[string]interface{}{"error": err.Error()}), nil
	}

	if !ocrResult.Success || len(ocrResult.Cups) == 0 {
		return sm.NewResult(sess.CurrentState).
			WithButtons(
				ocrFailureMessage(ocrResult.Error),
				ocrRetryButtons(birdClient)...,
			).
			WithEvent("ocr_failed", map[string]interface{}{"error": ocrResult.Error}), nil
	}

	// GUARDA (el CÓDIGO CUPS es prioridad para agendar): quedarse solo con los CUPS que traen
	// código. Un CUP sin código NO debe llegar al gate de cobertura, que lo interpretaría como
	// "sin convenio" y mandaría a particular (bug reportado: orden multipágina cuyo código quedó
	// vacío al leer solo la 1ª hoja). Si tras el fallback multipágina NINGÚN CUP trae código,
	// escalar a un agente (que sí puede leer la orden) en vez de mandar a particular.
	validCups := make([]services.CUPSEntry, 0, len(ocrResult.Cups))
	for _, c := range ocrResult.Cups {
		if strings.TrimSpace(c.Code) != "" {
			validCups = append(validCups, c)
		}
	}
	if len(validCups) == 0 {
		observability.Emit(observability.TraceSession(sess.ID), "agendar", "ocr_no_cups_code",
			observability.EmitOpts{Phone: sess.PhoneNumber})
		return sm.NewResult(sm.StateEscalateToAgent).
			WithText("No pudimos leer el código (CUPS) de tu orden. Te comunico con un agente para ayudarte a agendar tu cita.").
			WithEvent("ocr_no_cups_code", map[string]interface{}{"cups_count": len(ocrResult.Cups)}), nil
	}
	ocrResult.Cups = validCups

	// OCR exitoso — guardar CUPS en contexto
	cupsJSON, _ := json.Marshal(ocrResult.Cups)

	r := sm.NewResult(sm.StateValidateOCR).
		WithContext("ocr_cups_json", string(cupsJSON)).
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
			if birdClient != nil && !birdClient.HasAvailableAgents() {
				return sm.NewResult(sess.CurrentState).
					WithButtons(
						"En este momento no hay agentes disponibles. ¿Qué deseas hacer?",
						sm.Button{Text: "Enviar de nuevo", Payload: "retry_photo"},
					).
					WithEvent("agent_unavailable_at_escalation", nil), nil
			}
			return sm.NewResult(sm.StateEscalateToAgent).
				WithText("Te voy a comunicar con uno de nuestros agentes para que pueda ayudarte con tu orden médica.").
				WithEvent("ocr_escalate_to_agent", nil), nil
		}
	}

	return sm.RetryOrEscalate(sess, "Estoy esperando una *foto o PDF* de tu orden médica. Por favor envía el archivo."), nil
}

// VALIDATE_OCR (automático) — muestra resultados del OCR con verificación de documento
func validateOCRHandler(procedureRepo repository.ProcedureRepository) sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		var cups []services.CUPSEntry
		if err := json.Unmarshal([]byte(sess.GetContext("ocr_cups_json")), &cups); err != nil {
			return sm.NewResult(sm.StateEscalateToAgent).
				WithText("No pudimos procesar los datos de tu orden. Te voy a comunicar con un agente para que pueda ayudarte.").
				WithEvent("ocr_parse_error", map[string]interface{}{"error": err.Error()}), nil
		}

		// Validar CUPS contra la BD — filtrar inactivos, enriquecer nombres.
		// Si el código tiene sufijo numérico (ej "891509-16"), busca el código base interno.
		// Si el código no existe en el catálogo → se descarta; si no quedan procedimientos, pasa a agente.
		var skipped []string
		valid := cups[:0]
		for _, cup := range cups {
			if cup.Code != "" {
				proc, _ := procedureRepo.FindByCode(ctx, cup.Code)

				// Código base sin sufijo numérico (ej "891509-16" → "891509")
				if proc == nil || !proc.IsActive {
					if idx := strings.LastIndex(cup.Code, "-"); idx > 0 {
						if _, err2 := strconv.Atoi(cup.Code[idx+1:]); err2 == nil {
							baseCode := cup.Code[:idx]
							if p, e := procedureRepo.FindByCode(ctx, baseCode); e == nil && p != nil && p.IsActive {
								proc = p
								cup.Code = baseCode
							}
						}
					}
				}

				if proc == nil || !proc.IsActive {
					skipped = append(skipped, cup.Code)
					continue
				}
				cup.Name = proc.Name
			}
			valid = append(valid, cup)
		}
		cups = valid

		if len(cups) == 0 {
			observability.Emit(observability.TraceSession(sess.ID), "agendar", "cups_none",
				observability.EmitOpts{Phone: sess.PhoneNumber})
			return sm.NewResult(sm.StateEscalateToAgent).
				WithText("No pudimos procesar los procedimientos de tu orden médica. Te voy a comunicar con un agente para que pueda ayudarte.").
				WithEvent("ocr_no_valid_cups", map[string]interface{}{"skipped": skipped}), nil
		}

		// Re-serializar con datos enriquecidos
		cupsJSON, _ := json.Marshal(cups)

		// Construir resumen
		summary := "Detectamos los siguientes procedimientos en tu orden:\n\n"
		for i, cup := range cups {
			summary += fmt.Sprintf("%d. *%s*", i+1, cup.Name)
			qty := cup.Quantity
			if qty < 1 {
				qty = 1
			}
			if qty > 1 {
				summary += fmt.Sprintf(" — Cantidad: *%d*", qty)
			}
			summary += "\n"
		}

		if len(skipped) > 0 {
			summary += fmt.Sprintf("\n⚠️ %d procedimiento(s) de tu orden no están disponibles y fueron omitidos.\n", len(skipped))
		}

		summary += "\n¿Es correcto?"

		return sm.NewResult(sm.StateConfirmOCRResult).
			WithContext("ocr_cups_json", string(cupsJSON)).
			WithButtons(
				summary,
				sm.Button{Text: "Sí, correcto", Payload: "ocr_correct"},
				sm.Button{Text: "No, corregir", Payload: "ocr_incorrect"},
			), nil
	}
}

// CONFIRM_OCR_RESULT (interactivo) — usuario confirma o corrige
func confirmOCRResultHandler(procedureRepo repository.ProcedureRepository, birdClient *bird.Client) sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		result, selected := sm.ValidateButtonResponse(sess, msg, "ocr_correct", "ocr_incorrect")
		if result != nil {
			result.Messages = []sm.OutboundMessage{&sm.ButtonMessage{
				Text: "¿Es correcto?",
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
					WithEvent("ocr_parse_error", map[string]interface{}{"error": err.Error()}), nil
			}

			// Consolidación EMG/NC (Fisiatría): si la orden trae SOLO dependientes/NC (sin EMG), no se
			// agenda una cita independiente — se intenta consolidar con la cita EMG del paciente o pedir
			// la orden de la EMG (docs/DISENO-CONSOLIDACION-EMG-CITAS.md).
			if services.IsFisiatriaDependentOnly(cups) {
				return sm.NewResult(sm.StateCheckEmgConsolidation).
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
					WithEvent("ocr_no_valid_cups", nil), nil
			}

			groupsJSON, _ := json.Marshal(groups)

			r := sm.NewResult(sm.StateCheckSpecialCups).
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
					ocrRetryButtons(birdClient)...,
				).
				WithClearCtx("ocr_cups_json").
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
				ocrRetryButtons(birdClient)...,
			).
			WithEvent("ocr_failed_redirect", nil), nil
	}
}

// ASK_MANUAL_CUPS (interactivo) — usuario escribe nombre del procedimiento
func askManualCupsHandler(procedureRepo repository.ProcedureRepository) sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		input := strings.TrimSpace(msg.Text)

		if len(input) < 3 {
			retryResult := sm.ValidateWithRetry(sess, input, func(s string) bool { return len(s) >= 3 },
				"Por favor escribe al menos 3 caracteres del nombre del procedimiento.")
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
				WithClearCtx("ocr_cups_json").
				WithText(fmt.Sprintf("Procedimiento seleccionado: *%s*", proc.Name)).
				WithEvent("manual_cups_selected", map[string]interface{}{
					"code": proc.Code,
					"name": proc.Name,
				}), nil
		}

		// Múltiples resultados → mostrar lista
		procsJSON, _ := json.Marshal(procs)

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

		return sm.NewResult(sm.StateSelectProcedure).
			WithContext("search_procedures_json", string(procsJSON)).
			WithList(
				fmt.Sprintf("Encontramos *%d procedimientos*.\nSelecciona el correcto:", len(procs)),
				"Ver procedimientos",
				sm.ListSection{Title: "Procedimientos", Rows: rows},
			).
			WithEvent("procedure_search_results", map[string]interface{}{"count": len(procs)}), nil
	}
}

// SELECT_PROCEDURE (interactivo) — selección de procedimiento de la lista
func selectProcedureHandler() sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		if !msg.IsPostback {
			result := sm.RetryOrEscalate(sess, "Por favor selecciona un procedimiento de la lista, o escribe otro nombre para buscar de nuevo.")
			if result.NextState == sm.StateEscalateToAgent {
				return result, nil
			}
			return sm.NewResult(sm.StateAskManualCups).
				WithText("Por favor selecciona un procedimiento de la lista, o escribe otro nombre para buscar de nuevo."), nil
		}

		selectedID := msg.PostbackPayload

		// Buscar el procedimiento seleccionado
		var procs []struct {
			ID             int    `json:"ID"`
			Code           string `json:"Code"`
			Name           string `json:"Name"`
			ServiceName    string `json:"ServiceName"`
			RequiredSpaces int    `json:"RequiredSpaces"`
		}
		if err := json.Unmarshal([]byte(sess.GetContext("search_procedures_json")), &procs); err != nil {
			return sm.NewResult(sm.StateAskManualCups).
				WithText("No pudimos cargar los procedimientos. Por favor escribe el nombre de nuevo.").
				WithClearCtx("search_procedures_json"), nil
		}

		var selected *struct {
			ID             int    `json:"ID"`
			Code           string `json:"Code"`
			Name           string `json:"Name"`
			ServiceName    string `json:"ServiceName"`
			RequiredSpaces int    `json:"RequiredSpaces"`
		}
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
			WithClearCtx("search_procedures_json", "ocr_cups_json").
			WithText(fmt.Sprintf("Procedimiento seleccionado: *%s*", selected.Name)).
			WithEvent("manual_cups_selected", map[string]interface{}{
				"code": selected.Code,
				"name": selected.Name,
			}), nil
	}
}
