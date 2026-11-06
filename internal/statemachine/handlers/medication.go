package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/repository/local"
	"github.com/neuro-bot/neuro-bot/internal/services"
	"github.com/neuro-bot/neuro-bot/internal/session"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
	"github.com/neuro-bot/neuro-bot/internal/utils"
)

// RegisterMedicationHandlers registra los handlers del flujo de Aplicación de Medicamentos.
// medRepo puede ser nil (el bot informará que no hay listado disponible).
// ocrSvc puede ser nil (la orden se acepta sin validación de nombre).
func RegisterMedicationHandlers(m *sm.Machine, medRepo *local.MedicationPendingRepo, ocrSvc *services.OCRService) {
	m.Register(sm.StateMedicationCheckExcel, medicationCheckExcelHandler(medRepo))
	m.Register(sm.StateMedicationAskLastDate, medicationAskLastDateHandler())
	m.Register(sm.StateMedicationAskDose, medicationAskDoseHandler())
	m.Register(sm.StateMedicationAskFrequency, medicationAskFrequencyHandler())
	// Texto libre (C12): ante una palabra reservada el handler re-pregunta sin guardarla; el plan
	// no ofrece «volver a preguntar» en estos pasos.
	registerAgentField(sm.StateMedicationAskLastDate,
		agentTextInput("Fecha o tiempo de la última aplicación", sm.InputKindText, freeTextAnswer,
			"Escribe cuándo fue la última aplicación."))
	registerAgentField(sm.StateMedicationAskDose,
		agentTextInput("Dosis que se aplica", sm.InputKindText, freeTextAnswer, "Escribe la dosis."))
	registerAgentField(sm.StateMedicationAskFrequency,
		agentTextInput("Frecuencia de aplicación", sm.InputKindText, freeTextAnswer, "Escribe la frecuencia."))
	sm.RegisterStaticReturnMenu(sm.StateMedicationAskSymptoms,
		payloadOption("Sin síntomas", "sin_sintomas"),
		payloadOption("Con síntomas", "con_sintomas"),
	)
	m.RegisterWithConfig(sm.StateMedicationAskSymptoms, sm.HandlerConfig{
		InputType: sm.InputButton,
		Options:   []string{"sin_sintomas", "con_sintomas"},
		RetryPrompt: func(sess *session.Session, result *sm.StateResult) {
			// En el retorno sin dato del agente los botones solos no dicen qué se pregunta.
			if sm.IsAgentResumeTurn(sess) {
				result.Messages = append(result.Messages, &sm.TextMessage{Text: medicationSymptomsQuestion})
			}
			result.Messages = append(result.Messages, medicationSymptomsButtons())
		},
		Handler: medicationAskSymptomsHandler(),
	})
	m.Register(sm.StateMedicationReceiveHistoria, medicationReceiveHistoriaHandler())
	m.Register(sm.StateMedicationReceiveOrden, medicationReceiveOrdenHandler(ocrSvc))
	sm.RegisterStaticReturnMenu(sm.StateMedicationConfirmOrden,
		payloadOption("La orden es correcta", "med_confirm_ok"),
		payloadOption("Enviar la orden de nuevo", "med_confirm_retry"),
	)
	m.RegisterWithConfig(sm.StateMedicationConfirmOrden, sm.HandlerConfig{
		InputType: sm.InputButton,
		Options:   []string{"med_confirm_ok", "med_confirm_retry"},
		RetryPrompt: func(sess *session.Session, result *sm.StateResult) {
			// En el retorno sin dato del agente se repite qué no coincide: «¿Deseas continuar?» solo
			// no lo dice.
			if sm.IsAgentResumeTurn(sess) {
				result.Messages = append(result.Messages, &sm.TextMessage{Text: medicationOrderMismatchText(
					sess.GetContext("med_order_found_name"), sess.GetContext("med_medicamento"))})
			}
			result.Messages = append(result.Messages, medicationConfirmOrdenButtons())
		},
		Handler: medicationConfirmOrdenHandler(),
	})
	m.Register(sm.StateMedicationPrepareSchedule, medicationPrepareScheduleHandler())
	// Paso automático sin campo: devolverlo tal cual lo re-ejecuta y vuelve a escalar (P1-5).
	sm.HidePlainResume(sm.StateMedicationPrepareSchedule)
	sm.RegisterStaticReturnMenu(sm.StateMedicationPrepareSchedule,
		sm.ReturnOption{Label: "Volver al menú principal", Value: "menu_principal", Target: sm.StateMainMenu},
	)
}

// MEDICATION_CHECK_EXCEL (automático) — busca al paciente en el listado de medicamentos pendientes.
func medicationCheckExcelHandler(medRepo *local.MedicationPendingRepo) sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, _ bird.InboundMessage) (*sm.StateResult, error) {
		doc := sess.GetContext("patient_doc")

		if medRepo == nil {
			return backToMenuNoRecord(sess), nil
		}

		rec, err := medRepo.FindByDocument(ctx, doc)
		if err != nil || rec == nil {
			return backToMenuNoRecord(sess), nil
		}

		// Verificar autorización antes de continuar
		if !rec.HasAutorizacion() {
			medDesc := buildMedDescription(rec)
			list := &sm.ListMessage{
				Body: fmt.Sprintf(
					"Encontramos tu medicamento en nuestro listado:\n\n%s\n\n"+
						"⚠️ *Tu medicamento aún no tiene autorización vigente.*\n\n"+
						"Para poder agendar la cita de aplicación, debes dirigirte a tu EPS y solicitar la *autorización del medicamento*. "+
						"Una vez obtenida la autorización, vuelve a contactarnos y con gusto te agendaremos. ¿En qué más puedo ayudarte?",
					medDesc,
				),
				Title: "Ver opciones",
				Sections: []sm.ListSection{{
					Title: "Menú principal",
					Rows: []sm.ListRow{
						{ID: "agendar", Title: "Agendar cita", Description: "Si tienes una orden médica"},
						{ID: "consultar", Title: "Citas Programadas", Description: "Consulta, confirma o cancela tus citas"},
						{ID: "ubicacion", Title: "Ubicación", Description: "Conoce nuestras sedes"},
						{ID: "ayuda", Title: "Cómo usar el bot", Description: "Guía rápida"},
					},
				}},
			}
			r := sm.NewResult(sm.StateMainMenu).
				WithClearCtx("medication_flow").
				WithEvent("medication_sin_autorizacion", map[string]interface{}{
					"doc":         utils.MaskDocument(sess.GetContext("patient_doc")),
					"medicamento": rec.Medicamento,
				})
			r.Messages = append(r.Messages, list)
			return r, nil
		}

		// Guardar datos del medicamento en sesión
		sess.SetContext("med_medicamento", rec.Medicamento)
		sess.SetContext("med_nombre_comercial", rec.NombreComercial)
		sess.SetContext("med_concentracion", rec.Concentracion)
		sess.SetContext("med_forma_farmaceutica", rec.FormaFarmaceutica)
		sess.SetContext("med_saldo", fmt.Sprintf("%d", rec.SaldoActual))

		// Mostrar info del medicamento y preguntar fecha de última aplicación
		medDesc := buildMedDescription(rec)
		msg := fmt.Sprintf(
			"✅ Encontramos tu medicamento en nuestro listado:\n\n%s\n\n"+
				"Para continuar con la solicitud necesito algunos datos clínicos.\n\n"+
				"📅 ¿Cuándo fue la *última vez* que te aplicaste este medicamento?\n"+
				"_(Escribe la fecha, ej: 15/08/2026 o \"hace 6 meses\")_",
			medDesc,
		)
		return sm.NewResult(sm.StateMedicationAskLastDate).
			WithText(msg).
			WithEvent("medication_excel_found", map[string]interface{}{"medicamento": rec.Medicamento}), nil
	}
}

func backToMenuNoRecord(sess *session.Session) *sm.StateResult {
	list := &sm.ListMessage{
		Body: "Consultamos nuestro listado y *tu medicamento aún no está disponible en la farmacia institucional de Neurodx*.\n\n" +
			"Para continuar, debes dirigirte a tu entidad responsable o a la farmacia encargada de la dispensación y solicitar que el medicamento sea *enviado a Neurodx* para su posterior aplicación.\n\n" +
			"Una vez confirmada la disponibilidad, podrás agendar tu cita de aplicación. ¿En qué más puedo ayudarte?",
		Title: "Ver opciones",
		Sections: []sm.ListSection{{
			Title: "Menú principal",
			Rows: []sm.ListRow{
				{ID: "agendar", Title: "Agendar cita", Description: "Si tienes una orden médica"},
				{ID: "consultar", Title: "Citas Programadas", Description: "Consulta, confirma o cancela tus citas"},
				{ID: "ubicacion", Title: "Ubicación", Description: "Conoce nuestras sedes"},
				{ID: "ayuda", Title: "Cómo usar el bot", Description: "Guía rápida"},
			},
		}},
	}
	r := sm.NewResult(sm.StateMainMenu).
		WithClearCtx("medication_flow").
		WithEvent("medication_not_in_list", map[string]interface{}{"doc": utils.MaskDocument(sess.GetContext("patient_doc"))})
	r.Messages = append(r.Messages, list)
	return r
}

func buildMedDescription(rec *local.MedicationPendingRecord) string {
	var parts []string
	parts = append(parts, fmt.Sprintf("💊 *%s*", rec.Medicamento))
	if rec.NombreComercial != "" {
		parts = append(parts, fmt.Sprintf("   Nombre comercial: %s", rec.NombreComercial))
	}
	if rec.Concentracion != "" {
		parts = append(parts, fmt.Sprintf("   Concentración: %s", rec.Concentracion))
	}
	if rec.FormaFarmaceutica != "" {
		parts = append(parts, fmt.Sprintf("   Forma farmacéutica: %s", rec.FormaFarmaceutica))
	}
	if rec.EPS != "" {
		parts = append(parts, fmt.Sprintf("   EPS: %s", rec.EPS))
	}
	return strings.Join(parts, "\n")
}

// MEDICATION_ASK_LAST_DATE (interactivo) — recibe la fecha de última aplicación.
func medicationAskLastDateHandler() sm.StateHandler {
	return func(_ context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		text := strings.TrimSpace(msg.Text)
		// C12: una palabra reservada (__resume__ al volver del agente) cuenta como vacía.
		if !freeTextAnswer(text) {
			return sm.NewResult(sess.CurrentState).
				WithText("Por favor escribe la fecha de tu última aplicación. Ejemplo: *15/08/2026* o *\"hace 6 meses\"*."), nil
		}
		sess.SetContext("med_last_date", text)
		return sm.NewResult(sm.StateMedicationAskDose).
			WithText("💉 ¿Cuál es la *dosis* que te aplicas?\n_(Ej: 1 jeringa de 40mg, 2 viales, etc.)_"), nil
	}
}

// MEDICATION_ASK_DOSE (interactivo) — recibe la dosis.
func medicationAskDoseHandler() sm.StateHandler {
	return func(_ context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		text := strings.TrimSpace(msg.Text)
		// C12: una palabra reservada (__resume__ al volver del agente) cuenta como vacía.
		if !freeTextAnswer(text) {
			return sm.NewResult(sess.CurrentState).
				WithText("Por favor escribe la dosis. Ejemplo: *1 jeringa de 40mg*."), nil
		}
		sess.SetContext("med_dose", text)
		return sm.NewResult(sm.StateMedicationAskFrequency).
			WithText("🔁 ¿Con qué *frecuencia* te aplicas el medicamento?\n_(Ej: cada mes, cada 3 meses, semanal, etc.)_"), nil
	}
}

// MEDICATION_ASK_FREQUENCY (interactivo) — recibe la frecuencia y transiciona a síntomas.
func medicationAskFrequencyHandler() sm.StateHandler {
	return func(_ context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		text := strings.TrimSpace(msg.Text)
		// C12: una palabra reservada (__resume__ al volver del agente) cuenta como vacía.
		if !freeTextAnswer(text) {
			return sm.NewResult(sess.CurrentState).
				WithText("Por favor escribe la frecuencia. Ejemplo: *cada mes*."), nil
		}
		sess.SetContext("med_frequency", text)

		r := sm.NewResult(sm.StateMedicationAskSymptoms).
			WithText(medicationSymptomsQuestion)
		r.Messages = append(r.Messages, medicationSymptomsButtons())
		return r, nil
	}
}

// medicationSymptomsQuestion es la pregunta de MEDICATION_ASK_SYMPTOMS; sus botones van aparte.
const medicationSymptomsQuestion = "🌡️ *Revisión de síntomas*\n\n" +
	"En la *última semana*, ¿has presentado alguno de estos síntomas?\n\n" +
	"• Fiebre\n• Vómito\n• Diarrea\n• Congestión nasal"

func medicationSymptomsButtons() *sm.ButtonMessage {
	return &sm.ButtonMessage{
		Text: "Selecciona una opción:",
		Buttons: []sm.Button{
			{Text: "✅ No, ninguno", Payload: "sin_sintomas"},
			{Text: "❌ Sí, tengo alguno", Payload: "con_sintomas"},
		},
	}
}

// MEDICATION_ASK_SYMPTOMS (interactivo) — valida síntomas con botones.
func medicationAskSymptomsHandler() sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, _ bird.InboundMessage) (*sm.StateResult, error) {
		switch sm.ValidatedPayload(ctx) {
		case "con_sintomas":
			list := &sm.ListMessage{
				Body: "⚠️ Lo sentimos, por tu seguridad *no podemos aplicar el medicamento* mientras presentas estos síntomas.\n\n" +
					"Te recomendamos consultar a tu médico tratante y, una vez recuperado/a, comunicarte nuevamente con nosotros.\n\n" +
					"¿En qué más puedo ayudarte?",
				Title: "Ver opciones",
				Sections: []sm.ListSection{{
					Title: "Menú principal",
					Rows: []sm.ListRow{
						{ID: "agendar", Title: "Agendar cita", Description: "Si tienes una orden médica"},
						{ID: "consultar", Title: "Citas Programadas", Description: "Consulta o cancela citas"},
						{ID: "ubicacion", Title: "Ubicación", Description: "Conoce nuestras sedes"},
					},
				}},
			}
			r := sm.NewResult(sm.StateMainMenu).
				WithClearCtx("medication_flow").
				WithEvent("medication_symptoms_present", nil)
			r.Messages = append(r.Messages, list)
			return r, nil

		case "sin_sintomas":
			return sm.NewResult(sm.StateMedicationReceiveHistoria).
				WithText("📋 Perfecto. Ahora necesito que envíes los documentos.\n\n" +
					"1️⃣ *Historia clínica* — envía una foto o PDF."), nil

		default:
			r := sm.NewResult(sess.CurrentState)
			r.Messages = append(r.Messages, medicationSymptomsButtons())
			return r, nil
		}
	}
}

// MEDICATION_RECEIVE_HISTORIA (interactivo) — recibe foto/PDF de la historia clínica.
func medicationReceiveHistoriaHandler() sm.StateHandler {
	return func(_ context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		mediaURL := mediaURLFromMessage(msg)
		if mediaURL == "" {
			return sm.NewResult(sess.CurrentState).
				WithText("Por favor envía la *historia clínica* como foto o PDF."), nil
		}
		sess.SetContext("med_historia_url", mediaURL)
		return sm.NewResult(sm.StateMedicationReceiveOrden).
			WithText("✅ Historia clínica recibida.\n\n" +
				"2️⃣ Ahora envía la *orden médica* (foto o PDF)."), nil
	}
}

// MEDICATION_RECEIVE_ORDEN (interactivo) — recibe foto/PDF de la orden médica.
// Si hay OCR disponible, valida que el medicamento de la orden coincida con el autorizado.
// Si no hay CUPS en la orden, intenta validar por nombre del medicamento.
func medicationReceiveOrdenHandler(ocrSvc *services.OCRService) sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		mediaURL := mediaURLFromMessage(msg)
		if mediaURL == "" {
			return sm.NewResult(sess.CurrentState).
				WithText("Por favor envía la *orden médica* como foto o PDF."), nil
		}
		sess.SetContext("med_orden_url", mediaURL)

		// Sin OCR disponible: aceptar sin validar.
		if ocrSvc == nil {
			return sm.NewResult(sm.StateMedicationPrepareSchedule).
				WithText("✅ Documentos recibidos. Buscando disponibilidad para tu cita de aplicación... 🔍"), nil
		}

		// Ejecutar OCR sobre la orden médica.
		ocrResult, err := ocrSvc.AnalyzeDocument(ctx, mediaURL)
		if err != nil || ocrResult == nil || !ocrResult.Success {
			slog.Warn("medication ocr failed, proceeding without validation",
				"session_id", sess.ID, "error", err)
			return sm.NewResult(sm.StateMedicationPrepareSchedule).
				WithText("✅ Documentos recibidos. Buscando disponibilidad para tu cita de aplicación... 🔍").
				WithEvent("medication_ocr_failed", nil), nil
		}

		// Extraer nombres de medicamentos del resultado OCR.
		// Las órdenes de medicamentos generalmente no tienen CUPS, así que el nombre
		// aparece en cups_name con cups_code vacío (regla 3 del prompt OCR).
		foundName := extractMedicationNameFromOCR(ocrResult)
		pendingName := sess.GetContext("med_medicamento")
		comercialName := sess.GetContext("med_nombre_comercial")

		if foundName == "" {
			// OCR no encontró ningún medicamento legible: proceder sin bloquear.
			slog.Info("medication ocr: no medication name found, proceeding",
				"session_id", sess.ID)
			return sm.NewResult(sm.StateMedicationPrepareSchedule).
				WithText("✅ Documentos recibidos. Buscando disponibilidad para tu cita de aplicación... 🔍").
				WithEvent("medication_ocr_no_name", nil), nil
		}

		// Validar si el nombre encontrado en la orden coincide con el medicamento autorizado.
		if medicationNamesMatch(foundName, pendingName) || medicationNamesMatch(foundName, comercialName) {
			// Coincidencia clara: continuar directamente.
			return sm.NewResult(sm.StateMedicationPrepareSchedule).
				WithText(fmt.Sprintf("✅ Orden validada: *%s*\n\nBuscando disponibilidad para tu cita de aplicación... 🔍", foundName)).
				WithEvent("medication_order_validated", map[string]interface{}{"found": foundName, "method": "name_match"}), nil
		}

		// No coincide: pedir confirmación antes de continuar.
		sess.SetContext("med_order_found_name", foundName)
		r := sm.NewResult(sm.StateMedicationConfirmOrden).
			WithText(medicationOrderMismatchText(foundName, pendingName)).
			WithEvent("medication_order_mismatch", map[string]interface{}{"found": foundName, "pending": pendingName})
		r.Messages = append(r.Messages, medicationConfirmOrdenButtons())
		return r, nil
	}
}

// MEDICATION_CONFIRM_ORDEN (interactivo) — el paciente confirma o rechaza la orden cuando
// el nombre del medicamento no coincide exactamente con el de su autorización.
func medicationConfirmOrdenHandler() sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, _ bird.InboundMessage) (*sm.StateResult, error) {
		switch sm.ValidatedPayload(ctx) {
		case "med_confirm_ok":
			foundName := sess.GetContext("med_order_found_name")
			return sm.NewResult(sm.StateMedicationPrepareSchedule).
				WithText("✅ Continuando con tu solicitud. Buscando disponibilidad para tu cita de aplicación... 🔍").
				WithEvent("medication_order_confirmed_by_patient", map[string]interface{}{"found": foundName}), nil

		case "med_confirm_retry":
			return sm.NewResult(sm.StateMedicationReceiveOrden).
				WithClearCtx("med_order_found_name").
				WithText("Por favor envía nuevamente la *orden médica* correcta (foto o PDF).").
				WithEvent("medication_order_retry", nil), nil

		default:
			r := sm.NewResult(sess.CurrentState)
			r.Messages = append(r.Messages, medicationConfirmOrdenButtons())
			return r, nil
		}
	}
}

// medicationOrderMismatchText es la pregunta de MEDICATION_CONFIRM_ORDEN: el medicamento de la
// orden no coincide con el autorizado.
func medicationOrderMismatchText(foundName, pendingName string) string {
	return fmt.Sprintf(
		"⚠️ El medicamento encontrado en la orden es:\n*%s*\n\n"+
			"El medicamento registrado en tu autorización es:\n*%s*\n\n"+
			"¿Deseas continuar de todas formas?",
		foundName, pendingName,
	)
}

func medicationConfirmOrdenButtons() *sm.ButtonMessage {
	return &sm.ButtonMessage{
		Text: "¿Deseas continuar?",
		Buttons: []sm.Button{
			{Text: "✅ Sí, continuar", Payload: "med_confirm_ok"},
			{Text: "📎 Subir otra orden", Payload: "med_confirm_retry"},
		},
	}
}

// MEDICATION_PREPARE_SCHEDULE (automático) — determina el CUPS correcto según el medicamento
// y configura procedures_json para StateSearchSlots. Usar el CUPS adecuado garantiza que la
// búsqueda de horarios encuentre al médico asignado a ese tipo de medicamento.
func medicationPrepareScheduleHandler() sm.StateHandler {
	return func(_ context.Context, sess *session.Session, _ bird.InboundMessage) (*sm.StateResult, error) {
		medName := sess.GetContext("med_medicamento")
		cupsCode, cupsName := medicationCUPSFromName(medName)

		group := services.CUPSGroup{
			ServiceType: "Aplicacion Medicamentos",
			Cups: []services.CUPSEntry{
				{Code: cupsCode, Name: cupsName, Quantity: 1},
			},
			Espacios: 1,
		}
		procJSON, err := json.Marshal([]services.CUPSGroup{group})
		if err != nil {
			return sm.NewResult(sm.StateEscalateToAgent).
				WithText("Ocurrió un error preparando tu solicitud. Te conecto con un agente. 😊").
				WithContext(sm.CtxEscalationReason, sm.EscalationReasonMedicationError), nil
		}

		return sm.NewResult(sm.StateCheckExisting).
			WithContext("cups_code", cupsCode).
			WithContext("procedures_json", string(procJSON)).
			WithContext("current_procedure_idx", "0").
			WithClearCtx("medication_flow").
			WithEvent("medication_prepare_schedule", map[string]interface{}{
				"medicamento": medName,
				"cups_code":   cupsCode,
			}), nil
	}
}

// medicationCUPSFromName determina el CUPS apropiado para una aplicación de medicamento
// según el nombre del principio activo. El catálogo tiene 4 CUPS de aplicación, cada uno
// asignado a médicos distintos.
//
// 992801 — modificadores de respuesta biológica (adalimumab, tocilizumab, etc.)
// 992201 — sustancias hormonales (insulina, eritropoyetina, factor VIII, etc.)
// 992102 — antibióticos inyectables (ceftriaxona, vancomicina, meropenem, etc.)
// 992990 — otras sustancias (hierro sacarato, zoledronato, etc.) — default
func medicationCUPSFromName(medName string) (code, name string) {
	upper := strings.ToUpper(medName)
	switch {
	case isBiologicModifier(upper):
		return "992801", "INYECCIÓN O INFUSIÓN DE MODIFICADOR DE RESPUESTA BIOLÓGICA"
	case isHormonalSubstance(upper):
		return "992201", "INYECCIÓN O INFUSIÓN DE SUSTANCIA HORMONAL"
	case isAntibiotic(upper):
		return "992102", "INYECCIÓN O INFUSIÓN DE ANTIBIÓTICO"
	default:
		return "992990", "INYECCIÓN O INFUSIÓN DE OTRA SUSTANCIA TERAPÉUTICA O PROFILÁCTICA"
	}
}

// isBiologicModifier detecta modificadores de respuesta biológica por principio activo.
func isBiologicModifier(upper string) bool {
	biologics := []string{
		// TNF inhibidores
		"ADALIMUMAB", "INFLIXIMAB", "ETANERCEPT", "CERTOLIZUMAB", "GOLIMUMAB",
		// IL-6 inhibidores
		"TOCILIZUMAB", "SARILUMAB",
		// IL-17/23 inhibidores
		"SECUKINUMAB", "IXEKIZUMAB", "USTEKINUMAB", "RISANKIZUMAB", "GUSELKUMAB",
		// IL-4/13 inhibidor
		"DUPILUMAB", "TRALOKINUMAB",
		// IL-5 inhibidores
		"MEPOLIZUMAB", "RESLIZUMAB", "BENRALIZUMAB", "TEZEPELUMAB",
		// IgE inhibidor
		"OMALIZUMAB",
		// B-cell
		"RITUXIMAB", "OBINUTUZUMAB", "BELIMUMAB", "ANIFROLUMAB",
		// T-cell
		"ABATACEPT",
		// Hueso
		"ROMOSOZUMAB", "DENOSUMAB",
		// PCSK9 inhibidores
		"EVOLOCUMAB", "ALIROCUMAB", "INCLISIRAN",
		// CGRP (migraña)
		"ERENUMAB", "FREMANEZUMAB", "GALCANEZUMAB", "EPTINEZUMAB",
		// CD20 / linfomas
		"OFATUMUMAB", "OCRELIZUMAB",
		// Otros biologicos
		"VEDOLIZUMAB", "NATALIZUMAB", "ALEMTUZUMAB",
		"BIMEKIZUMAB", "SPESOLIMAB",
	}
	for _, b := range biologics {
		if strings.Contains(upper, b) {
			return true
		}
	}
	return false
}

// isHormonalSubstance detecta sustancias hormonales e insulinas.
func isHormonalSubstance(upper string) bool {
	hormones := []string{
		"INSULINA", "INSULIN",
		"TESTOSTERONA", "TESTOSTERONE",
		"ESTRADIOL", "PROGESTERONA",
		"SOMATROPINA", "SOMATROPIN",
		"ERITROPOYETINA", "EPOETINA", "DARBEPOETINA", "DARBEPOETIN",
		"PEGFILGRASTIM", "FILGRASTIM", "LENOGRASTIM",
		"TERIPARATIDE", "TERIPARATIDA",
		"LANREOTIDE", "OCTREOTIDE", "PASIREOTIDE",
		"FACTOR VIII", "FACTOR VII", "FACTOR IX", "FACTOR X",
		"ELTROMBOPAG", "ROMIPLOSTIM",
	}
	for _, h := range hormones {
		if strings.Contains(upper, h) {
			return true
		}
	}
	return false
}

// isAntibiotic detecta antibióticos de uso inyectable.
func isAntibiotic(upper string) bool {
	antibiotics := []string{
		"CEFTRIAXONA", "CEFAZOLINA", "CEFUROXIMA", "CEFOTAXIMA", "CEFEPIMA",
		"VANCOMICINA", "TEICOPLANINA", "DAPTOMICINA",
		"MEROPENEM", "IMIPENEM", "ERTAPENEM", "DORIPENEM",
		"PIPERACILINA", "AMPICILINA", "AMOXICILINA",
		"AZITROMICINA", "CLARITROMICINA",
		"CIPROFLOXACINA", "LEVOFLOXACINA", "MOXIFLOXACINA",
		"CLINDAMICINA", "METRONIDAZOL",
		"COLISTINA", "POLIMIXINA",
		"LINEZOLID", "TIGECICLINA",
		"GENTAMICINA", "AMIKACINA", "TOBRAMICINA",
	}
	for _, a := range antibiotics {
		if strings.Contains(upper, a) {
			return true
		}
	}
	return false
}

func mediaURLFromMessage(msg bird.InboundMessage) string {
	switch msg.MessageType {
	case "image":
		return msg.ImageURL
	case "document":
		return msg.DocumentURL
	}
	return ""
}

// extractMedicationNameFromOCR extrae el nombre del medicamento del resultado OCR.
// Busca en las entradas CUPS la primera con nombre no vacío; las órdenes de medicamentos
// típicamente tienen cups_code vacío y el nombre del medicamento en cups_name.
func extractMedicationNameFromOCR(result *services.OCRResult) string {
	if result == nil {
		return ""
	}
	for _, c := range result.Cups {
		name := strings.TrimSpace(c.Name)
		if name != "" {
			return name
		}
	}
	return ""
}

// medicationNamesMatch compara el nombre de un medicamento extraído de la orden con el
// nombre autorizado. Usa el principio activo (primera palabra alfabética significativa)
// como clave de comparación, con fallback a substring.
func medicationNamesMatch(orderName, pendingName string) bool {
	if orderName == "" || pendingName == "" {
		return false
	}
	norm := func(s string) string {
		s = strings.ToUpper(strings.TrimSpace(s))
		s = strings.ReplaceAll(s, ",", ".")
		return s
	}
	o := norm(orderName)
	p := norm(pendingName)

	// Extraer primer token alfabético de al menos 4 caracteres (principio activo).
	principalIngredient := func(s string) string {
		for _, w := range strings.Fields(s) {
			clean := onlyAlpha(w)
			if len(clean) >= 4 {
				return clean
			}
		}
		return ""
	}

	oPI := principalIngredient(o)
	pPI := principalIngredient(p)

	// Coincidencia por principio activo (ej: "ADALIMUMAB" == "ADALIMUMAB").
	if oPI != "" && pPI != "" && oPI == pPI {
		return true
	}

	// Fallback: uno contiene al otro completamente (ej: orden abreviada vs nombre completo).
	if strings.Contains(o, p) || strings.Contains(p, o) {
		return true
	}

	return false
}

// onlyAlpha retorna solo los caracteres A-Z del string.
func onlyAlpha(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 'A' && r <= 'Z' {
			b.WriteRune(r)
		}
	}
	return b.String()
}
