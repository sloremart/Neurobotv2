package handlers

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/observability"
	"github.com/neuro-bot/neuro-bot/internal/repository"
	"github.com/neuro-bot/neuro-bot/internal/services"
	"github.com/neuro-bot/neuro-bot/internal/session"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
	"github.com/neuro-bot/neuro-bot/internal/statemachine/validators"
)

// RegisterMedicalValidationHandlers registra los handlers de validaciones médicas (Fase 9).
func RegisterMedicalValidationHandlers(m *sm.Machine, gfrSvc *services.GFRService, apptSvc *services.AppointmentService, procRepo repository.ProcedureRepository) {
	m.Register(sm.StateCheckSpecialCups, checkSpecialCupsHandler())
	m.Register(sm.StateAskGestationalWeeks, askGestationalWeeksHandler())
	m.Register(sm.StateAskContrasted, askContrastedHandler())
	m.Register(sm.StateAskPregnancy, askPregnancyHandler())
	m.Register(sm.StatePregnancyBlock, pregnancyBlockHandler())
	m.Register(sm.StateAskBabyWeight, askBabyWeightHandler())
	m.Register(sm.StateGfrCreatinine, gfrCreatinineHandler())
	m.Register(sm.StateGfrExamDate, gfrExamDateHandler())
	m.Register(sm.StateGfrDisease, gfrDiseaseHandler())
	m.Register(sm.StateGfrHeight, gfrHeightHandler())
	m.RegisterWithConfig(sm.StateGfrWeight, sm.HandlerConfig{
		InputType:    sm.InputText,
		TextValidate: validators.FloatRange(10, 300),
		ErrorMsg:     "Peso no válido. Ingresa tu peso en kilogramos (ejemplo: 70).",
		Handler:      gfrWeightHandler(),
	})
	m.Register(sm.StateGfrResult, gfrResultHandler(gfrSvc, procRepo))
	m.Register(sm.StateGfrNotEligible, gfrNotEligibleHandler())
	m.Register(sm.StateAskSedation, askSedationHandler())
	m.Register(sm.StateCheckExisting, checkExistingHandler(apptSvc))
	m.Register(sm.StateAppointmentExists, appointmentExistsHandler())
	m.Register(sm.StateCheckPriorConsult, checkPriorConsultHandler(apptSvc))
	m.Register(sm.StateCheckMRCLimit, checkMRCLimitHandler(apptSvc))
	m.Register(sm.StateCheckAgeRestriction, checkAgeRestrictionHandler())

	registerMedicalRecovery(m)
}

// registerMedicalRecovery habilita la recuperación IA en los estados clínicos de entrada numérica /
// selección. Hints validados contra la API real (15/15): unidades y formato exactos que espera cada
// handler. Peso del bebé es selección (baby_low/baby_normal); el resto son números.
func registerMedicalRecovery(m *sm.Machine) {
	m.RegisterRecovery(sm.StateGfrCreatinine, sm.HandlerConfig{
		AIRecovery:  true,
		AIInputHint: "El valor de CREATININA en sangre, en mg/dL (número decimal entre 0 y 30). v = solo el número decimal (usa punto).",
	})
	m.RegisterRecovery(sm.StateGfrExamDate, sm.HandlerConfig{
		AIRecovery:  true,
		AIInputHint: "La FECHA DE TOMA del examen de creatinina. v = la fecha en formato AAAA-MM-DD.",
	})
	m.RegisterRecovery(sm.StateGfrHeight, sm.HandlerConfig{
		AIRecovery:  true,
		AIInputHint: "La ESTATURA del paciente en CENTÍMETROS. Si la dan en metros, conviértela (1.70 m → 170). v = solo el número entero en cm.",
	})
	m.RegisterRecovery(sm.StateGfrWeight, sm.HandlerConfig{
		AIRecovery:  true,
		AIInputHint: "El PESO del paciente en KILOGRAMOS. v = solo el número (usa punto para decimales).",
	})
	m.RegisterRecovery(sm.StateAskGestationalWeeks, sm.HandlerConfig{
		AIRecovery:  true,
		AIInputHint: "Las SEMANAS DE GESTACIÓN. v = el número de semanas; usa punto para los días (13 semanas y 6 días → 13.6).",
	})
	m.RegisterRecovery(sm.StateAskBabyWeight, sm.HandlerConfig{
		AIRecovery:  true,
		AIInputHint: "El PESO DEL BEBÉ al nacer. Opciones: baby_low (bajo peso, menos de 2.5 kg) o baby_normal (2.5 kg o más). v = exactamente 'baby_low' o 'baby_normal'.",
	})
}

// --- Helpers ---

// isContrastable determina si un CUPS puede ser contrastado (resonancias, tomografías).
// 883xxx = Resonancia Magnética, 871xxx/879xxx = Tomografía (TAC).
// 921xxx = PET/CT — usa radiotrazadores, NUNCA contraste yodado → excluido.
func isContrastable(cupsCode string) bool {
	return strings.HasPrefix(cupsCode, "883") ||
		strings.HasPrefix(cupsCode, "871") ||
		strings.HasPrefix(cupsCode, "879")
}

// isPETCT retorna true para estudios PET/CT (921xxx): usan radiotrazadores, nunca contraste yodado.
// Aunque el OCR marque ocr_is_contrasted=1 (confunde "CON 18-FLUOR"/"CON PSMA" con contraste),
// el flujo de creatinina/GFR no aplica y debe saltarse.
func isPETCT(cupsCode string) bool {
	return strings.HasPrefix(cupsCode, "921")
}

// isSedatable determina si un CUPS puede requerir sedación (resonancias).
func isSedatable(cupsCode string) bool {
	return strings.HasPrefix(cupsCode, "883")
}

// --- Handlers ---

// ASK_CONTRASTED (automático) — pregunta si requiere contraste.
// Si el CUPS no es contrastable, auto-chains to next state.
// If contrastable, shows buttons and returns self (auto-chain stops on same state).
func askContrastedHandler() sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		cupsCode := sess.GetContext("cups_code")

		// CUPS no contrastable → saltar SOLO si el OCR tampoco marcó contraste (M5).
		// Antes se salía solo por el prefijo del cups_code, descartando ocr_is_contrasted=1: si el
		// código representativo del grupo no era contrastable (p.ej. sedación 998702) en una orden de
		// resonancia CONTRASTADA, se saltaban los gates de seguridad (función renal/TFG y embarazo).
		//
		// Excepción PET/CT (921xxx): usan radiotrazadores (18-F FDG, PSMA…), NUNCA contraste yodado.
		// El OCR puede marcar ocr_is_contrasted=1 al ver "CON 18-FLUOR"/"CON PSMA" en el nombre —
		// esa señal se ignora y siempre se salta el flujo de contraste/creatinina.
		if isPETCT(cupsCode) || (!isContrastable(cupsCode) && sess.GetContext("ocr_is_contrasted") != "1") {
			return sm.NewResult(sm.StateAskSedation).
				WithContext("is_contrasted", "0").
				WithClearCtx("ocr_is_contrasted").
				WithEvent("contrast_skipped", map[string]interface{}{"cups_code": cupsCode}), nil
		}

		// Si el OCR ya detectó contraste en la orden, auto-completar
		if sess.GetContext("ocr_is_contrasted") == "1" {
			gender := sess.GetContext("patient_gender")
			age, _ := strconv.Atoi(sess.GetContext("patient_age"))

			r := sm.NewResult("").
				WithContext("is_contrasted", "1").
				WithClearCtx("ocr_is_contrasted").
				WithText("Tu orden indica que el examen requiere *medio de contraste*. Lo tendremos en cuenta.").
				WithEvent("contrast_auto_detected", map[string]interface{}{"cups_code": cupsCode})

			if gender == "F" && age >= 12 && age <= 55 {
				r.NextState = sm.StateAskPregnancy
			} else if age < 1 {
				r.NextState = sm.StateAskBabyWeight
				r.Messages = append(r.Messages, &sm.ButtonMessage{
					Text: "¿Cuál fue el peso del bebé al nacer?",
					Buttons: []sm.Button{
						{Text: "Bajo peso", Payload: "baby_low"},
						{Text: "Peso normal", Payload: "baby_normal"},
					},
				})
			} else {
				r.NextState = sm.StateGfrCreatinine
				r.Messages = append(r.Messages, &sm.TextMessage{Text: "Para el examen con contraste necesitamos verificar tu *función renal*.\n\nNecesitas un examen de laboratorio reciente (máximo 30 días) llamado *\"Creatinina sérica\"* o *\"Creatinina en sangre\"*.\n\nEn tu resultado de laboratorio busca el valor que aparece junto a *Creatinina* en *mg/dL*.\n\n_Ejemplo de resultado:_\n_Creatinina .......... *0.96* mg/dL_\n\nEscribe solo el número, ej: *0.96*"})
			}

			return r, nil
		}

		// First entry: show prompt without burning a retry
		if sess.GetContext("_prompted_contrast") == "" {
			return sm.NewResult(sess.CurrentState).
				WithContext("_prompted_contrast", "1").
				WithButtons(
					"¿Tu examen requiere *medio de contraste*?\n\n(Esto debe indicarlo tu orden médica)",
					sm.Button{Text: "Sí, con contraste", Payload: "contrast_yes"},
					sm.Button{Text: "No, sin contraste", Payload: "contrast_no"},
				), nil
		}

		result, selected := sm.ValidateButtonResponse(sess, msg, "contrast_yes", "contrast_no")
		if result != nil {
			result.Messages = []sm.OutboundMessage{&sm.ButtonMessage{
				Text: "¿Tu examen requiere *medio de contraste*?\n\n(Esto debe indicarlo tu orden médica)",
				Buttons: []sm.Button{
					{Text: "Sí, con contraste", Payload: "contrast_yes"},
					{Text: "No, sin contraste", Payload: "contrast_no"},
				},
			}}
			return result, nil
		}

		switch selected {
		case "contrast_yes":
			gender := sess.GetContext("patient_gender")
			age, _ := strconv.Atoi(sess.GetContext("patient_age"))

			r := sm.NewResult("").
				WithContext("is_contrasted", "1").
				WithClearCtx("_prompted_contrast").
				WithEvent("contrast_selected", map[string]interface{}{"contrasted": true})

			if gender == "F" && age >= 12 && age <= 55 {
				// Mujer en edad fértil (12-55) → preguntar embarazo
				r.NextState = sm.StateAskPregnancy
			} else if age < 1 {
				// Bebé (any gender) → preguntar peso
				r.NextState = sm.StateAskBabyWeight
				r.WithButtons(
					"¿Cuál fue el peso del bebé al nacer?",
					sm.Button{Text: "Bajo peso", Payload: "baby_low"},
					sm.Button{Text: "Peso normal", Payload: "baby_normal"},
				)
			} else {
				// Hombre >= 1 → directo a creatinina
				r.NextState = sm.StateGfrCreatinine
				r.WithText("Para el examen con contraste necesitamos verificar tu *función renal*.\n\nNecesitas un examen de laboratorio reciente (máximo 30 días) llamado *\"Creatinina sérica\"* o *\"Creatinina en sangre\"*.\n\nEn tu resultado de laboratorio busca el valor que aparece junto a *Creatinina* en *mg/dL*.\n\n_Ejemplo de resultado:_\n_Creatinina .......... *0.96* mg/dL_\n\nEscribe solo el número, ej: *0.96*")
			}

			return r, nil

		case "contrast_no":
			return sm.NewResult(sm.StateAskSedation).
				WithContext("is_contrasted", "0").
				WithClearCtx("_prompted_contrast").
				WithEvent("contrast_selected", map[string]interface{}{"contrasted": false}), nil
		}

		return nil, fmt.Errorf("unreachable: selected=%s", selected)
	}
}

// ASK_PREGNANCY (automático) — solo para mujeres en edad fértil (12-55) con contraste.
// Auto-skips for males, children < 12, and women > 55.
func askPregnancyHandler() sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		gender := sess.GetContext("patient_gender")
		age, _ := strconv.Atoi(sess.GetContext("patient_age"))

		// Auto-skip: males or outside fertile age range (12-55)
		if gender != "F" || age < 12 || age > 55 {
			nextState := sm.StateGfrCreatinine
			r := sm.NewResult(nextState).
				WithContext("is_pregnant", "0")
			if age < 1 {
				nextState = sm.StateAskBabyWeight
				r.NextState = nextState
			} else {
				r.WithText("Para el examen con contraste necesitamos verificar tu *función renal*.\n\nNecesitas un examen de laboratorio reciente (máximo 30 días) llamado *\"Creatinina sérica\"* o *\"Creatinina en sangre\"*.\n\nEn tu resultado de laboratorio busca el valor que aparece junto a *Creatinina* en *mg/dL*.\n\n_Ejemplo de resultado:_\n_Creatinina .......... *0.96* mg/dL_\n\nEscribe solo el número, ej: *0.96*")
			}
			return r, nil
		}

		// First entry: show prompt
		if sess.GetContext("_prompted_pregnancy") == "" {
			return sm.NewResult(sess.CurrentState).
				WithContext("_prompted_pregnancy", "1").
				WithButtons(
					"¿Estás embarazada?",
					sm.Button{Text: "Sí", Payload: "pregnant_yes"},
					sm.Button{Text: "No", Payload: "pregnant_no"},
				), nil
		}

		result, selected := sm.ValidateButtonResponse(sess, msg, "pregnant_yes", "pregnant_no")
		if result != nil {
			result.Messages = []sm.OutboundMessage{&sm.ButtonMessage{
				Text: "¿Estás embarazada?",
				Buttons: []sm.Button{
					{Text: "Sí", Payload: "pregnant_yes"},
					{Text: "No", Payload: "pregnant_no"},
				},
			}}
			return result, nil
		}

		switch selected {
		case "pregnant_yes":
			observability.Emit(observability.TraceSession(sess.ID), "agendar", "pregnancy_blocked",
				observability.EmitOpts{Phone: sess.PhoneNumber})
			return sm.NewResult(sm.StatePregnancyBlock).
				WithContext("is_pregnant", "1").
				WithClearCtx("_prompted_pregnancy").
				WithEvent("pregnant_selected", map[string]interface{}{"pregnant": true}), nil
		case "pregnant_no":
			r := sm.NewResult(sm.StateGfrCreatinine).
				WithContext("is_pregnant", "0").
				WithClearCtx("_prompted_pregnancy").
				WithText("Para el examen con contraste necesitamos verificar tu *función renal*.\n\nNecesitas un examen de laboratorio reciente (máximo 30 días) llamado *\"Creatinina sérica\"* o *\"Creatinina en sangre\"*.\n\nEn tu resultado de laboratorio busca el valor que aparece junto a *Creatinina* en *mg/dL*.\n\n_Ejemplo de resultado:_\n_Creatinina .......... *0.96* mg/dL_\n\nEscribe solo el número, ej: *0.96*").
				WithEvent("pregnant_selected", map[string]interface{}{"pregnant": false})
			return r, nil
		}

		return nil, fmt.Errorf("unreachable: selected=%s", selected)
	}
}

// PREGNANCY_BLOCK (automático) — bloquea embarazada + contraste.
func pregnancyBlockHandler() sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		return buildAutoCloseResult("Por seguridad, no es posible realizar exámenes con *medio de contraste* durante el embarazo.\n\nConsulta con tu médico tratante para alternativas.").
			WithEvent("pregnant_blocked", nil), nil
	}
}

// ASK_BABY_WEIGHT (interactivo) — solo si edad < 1 y contrastado.
// Afecta el factor k en la fórmula de Schwartz.
func askBabyWeightHandler() sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		result, selected := sm.ValidateButtonResponse(sess, msg, "baby_low", "baby_normal")
		if result != nil {
			result.Messages = []sm.OutboundMessage{&sm.ButtonMessage{
				Text: "¿Cuál fue el peso del bebé al nacer?",
				Buttons: []sm.Button{
					{Text: "Bajo peso", Payload: "baby_low"},
					{Text: "Peso normal", Payload: "baby_normal"},
				},
			}}
			return result, nil
		}

		cat := "normal"
		if selected == "baby_low" {
			cat = "bajo"
		}

		return sm.NewResult(sm.StateGfrCreatinine).
			WithContext("baby_weight_cat", cat).
			WithText("Ingresa el valor de *creatinina* del bebé en mg/dL.\n\nEjemplo: 0.4").
			WithEvent("baby_weight_selected", map[string]interface{}{"category": cat}), nil
	}
}

// GFR_CREATININE (interactivo) — pide valor de creatinina.
func gfrCreatinineHandler() sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		input := strings.TrimSpace(msg.Text)

		value, err := strconv.ParseFloat(strings.Replace(input, ",", ".", 1), 64)
		if err != nil || value <= 0 || value > 30 {
			retryResult := sm.ValidateWithRetry(sess, "", func(string) bool { return false },
				"Valor no válido. Escribe solo el número de creatinina en mg/dL.\n\n_Ejemplo: si tu resultado dice Creatinina 0.96 mg/dL, escribe *0.96*_")
			return retryResult, nil
		}

		// Tras el valor, pedir la FECHA de toma del examen (vigencia de 30 días).
		return sm.NewResult(sm.StateGfrExamDate).
			WithContext("gfr_creatinine", fmt.Sprintf("%.2f", value)).
			WithText("¿Qué día te tomaste el examen de creatinina?\n\nEscríbelo como *DD/MM/AAAA* (por ejemplo *05/07/2026*).\n\nEl examen debe tener *máximo 30 días* al momento de tu cita."), nil
	}
}

// GFR_EXAM_DATE (interactivo) — fecha de TOMA del examen de creatinina. Con ella se calcula la vigencia
// (toma + 30 días) = la última fecha de cita válida con ese examen. Se le informa al paciente; si el
// examen ya venció, se le avisa que deberá llevar uno actualizado. NO bloquea. Luego, si elige un slot
// posterior al límite, se le recuerda de nuevo (showSlotsHandler) y en el recordatorio de WhatsApp.
func gfrExamDateHandler() sm.StateHandler {
	const dateHelp = "Escribe la *fecha de toma* del examen como *DD/MM/AAAA*, por ejemplo *05/07/2026*."
	return func(_ context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		input := strings.TrimSpace(msg.Text)

		// Mismo parser flexible que la fecha de nacimiento (ISO y DD/MM/AAAA, con - o /).
		examDate, ok := parseBirthDate(input)
		if !ok || examDate.After(time.Now()) {
			retryResult := sm.ValidateWithRetry(sess, "", func(string) bool { return false },
				"No entendí la fecha. "+dateHelp)
			return retryResult, nil
		}

		limit := examDate.AddDate(0, 0, 30)
		today := time.Now().Truncate(24 * time.Hour)

		var limitMsg string
		if limit.Before(today) {
			limitMsg = fmt.Sprintf(
				"⚠️ Tu examen de creatinina (tomado el *%s*) ya superó los *30 días* de vigencia. Puedes continuar agendando, pero deberás llevar un examen *actualizado* (máximo 30 días) el día de tu cita.",
				examDate.Format("02/01/2006"))
		} else {
			limitMsg = fmt.Sprintf(
				"Tu examen de creatinina es válido hasta el *%s* (30 días desde la toma). Puedes elegir una cita hasta esa fecha; si eliges una *posterior*, deberás repetir el examen para actualizarlo.",
				limit.Format("02/01/2006"))
		}

		age, _ := strconv.Atoi(sess.GetContext("patient_age"))
		r := sm.NewResult("").
			WithContext("gfr_exam_date", examDate.Format("2006-01-02")).
			WithContext("gfr_creatinine_limit", limit.Format("2006-01-02")).
			WithText(limitMsg)

		switch {
		case age <= 14:
			// Schwartz necesita altura
			r.NextState = sm.StateGfrHeight
			r.WithText("Escribe la altura del paciente en centímetros. (Ejemplo: si mide 1.70 m, escribir 170)")
		case age < 40:
			// 15-39: preguntar enfermedad
			r.NextState = sm.StateGfrDisease
			r.WithButtons(
				"¿Padeces alguna de estas condiciones?",
				sm.Button{Text: "Ninguna", Payload: "disease_none"},
				sm.Button{Text: "Enfermedad renal", Payload: "disease_renal"},
				sm.Button{Text: "Diabetes", Payload: "disease_diabetica"},
			)
		default:
			// >= 40: Cockcroft-Gault necesita peso
			r.NextState = sm.StateGfrWeight
			r.WithContext("gfr_disease_type", "disease_none")
			r.WithText("Ingresa tu *peso* en kilogramos.\n\nEjemplo: 70")
		}

		return r, nil
	}
}

// GFR_DISEASE (interactivo) — pregunta enfermedad (solo 15-39 años).
func gfrDiseaseHandler() sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		result, selected := sm.ValidateButtonResponse(sess, msg, "disease_none", "disease_renal", "disease_diabetica")
		if result != nil {
			result.Messages = []sm.OutboundMessage{&sm.ButtonMessage{
				Text: "¿Padeces alguna de estas condiciones?",
				Buttons: []sm.Button{
					{Text: "Ninguna", Payload: "disease_none"},
					{Text: "Enfermedad renal", Payload: "disease_renal"},
					{Text: "Diabetes", Payload: "disease_diabetica"},
				},
			}}
			return result, nil
		}

		r := sm.NewResult("").
			WithContext("gfr_disease_type", selected)

		if selected == "disease_none" {
			// CKD-EPI no necesita peso ni altura → calcular
			r.NextState = sm.StateGfrResult
		} else {
			// Cockcroft-Gault necesita peso
			r.NextState = sm.StateGfrWeight
			r.WithText("Ingresa tu *peso* en kilogramos.\n\nEjemplo: 70")
		}

		return r.WithEvent("disease_selected", map[string]interface{}{"type": selected}), nil
	}
}

// GFR_HEIGHT (interactivo) — pide estatura en cm.
func gfrHeightHandler() sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		input := strings.TrimSpace(msg.Text)
		value, err := strconv.ParseFloat(strings.Replace(input, ",", ".", 1), 64)
		if err != nil || value < 30 || value > 250 {
			retryResult := sm.ValidateWithRetry(sess, "", func(string) bool { return false },
				"Estatura no válida. Escribe la altura del paciente en centímetros. (Ejemplo: si mide 1.70 m, escribir 170)")
			return retryResult, nil
		}

		age, _ := strconv.Atoi(sess.GetContext("patient_age"))

		r := sm.NewResult("").
			WithContext("gfr_height_cm", fmt.Sprintf("%.0f", value))

		if age <= 14 {
			// Schwartz: solo necesita creatinina + altura → calcular
			r.NextState = sm.StateGfrResult
		} else {
			// Necesita peso también
			r.NextState = sm.StateGfrWeight
			r.WithText("Ingresa tu *peso* en kilogramos.\n\nEjemplo: 70")
		}

		return r, nil
	}
}

// GFR_WEIGHT — solo lógica de negocio (validación declarativa en RegisterWithConfig).
func gfrWeightHandler() sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		input := strings.TrimSpace(msg.Text)
		value, _ := strconv.ParseFloat(strings.Replace(input, ",", ".", 1), 64)
		return sm.NewResult(sm.StateGfrResult).
			WithContext("gfr_weight_kg", fmt.Sprintf("%.1f", value)), nil
	}
}

// nephroprotectionMsg es el protocolo de N-Acetilcisteína para tomografías con TFG 31-49.
// Solo aplica a estudios con contraste yodado (TAC, asunto_id=3); no aplica a resonancia.
const nephroprotectionMsg = "\n\n⚠️ *PROTOCOLO DE NEFROPROTECCIÓN CON N-ACETILCISTEÍNA*\n\n" +
	"*N-ACETILCISTEÍNA SOBRES X 600MG*\n" +
	"Diluir *dos sobres* de N-Acetilcisteína (dosis 1200 mg) en un vaso con agua y tomar *cada 12 horas*. " +
	"Iniciar la toma *24 horas antes* del estudio y continuar *24 horas posterior* al estudio."

// GFR_RESULT (automático) — calcula GFR y decide si procede.
// Para tomografías (asunto_id=3) con TFG 31-49, añade el protocolo de nefroprotección.
func gfrResultHandler(gfrSvc *services.GFRService, procRepo repository.ProcedureRepository) sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		age, _ := strconv.Atoi(sess.GetContext("patient_age"))
		gender := sess.GetContext("patient_gender")
		diseaseType := sess.GetContext("gfr_disease_type")
		babyWeightCat := sess.GetContext("baby_weight_cat")
		creatinine, _ := strconv.ParseFloat(sess.GetContext("gfr_creatinine"), 64)
		heightCm, _ := strconv.ParseFloat(sess.GetContext("gfr_height_cm"), 64)
		weightKg, _ := strconv.ParseFloat(sess.GetContext("gfr_weight_kg"), 64)

		result := gfrSvc.Calculate(age, gender, diseaseType, babyWeightCat, creatinine, heightCm, weightKg)

		msg := result.Message

		// TFG 31-49 en tomografía (asunto_id=3): añadir protocolo de nefroprotección.
		if result.Eligible && result.Value >= 31 && result.Value <= 49 && procRepo != nil {
			cupsCode := sess.GetContext("cups_code")
			if asuntoID, err := procRepo.FindSubjectTypeForCups(ctx, cupsCode); err == nil && asuntoID == 3 {
				msg += nephroprotectionMsg
			}
		}

		r := sm.NewResult("").
			WithContext("gfr_calculated", fmt.Sprintf("%.1f", result.Value)).
			WithText(msg).
			WithEvent("gfr_calculated", map[string]interface{}{
				"value":    result.Value,
				"formula":  result.Formula,
				"eligible": result.Eligible,
			})

		if !result.Eligible {
			r.NextState = sm.StateGfrNotEligible
			observability.Emit(observability.TraceSession(sess.ID), "agendar", "gfr_blocked",
				observability.EmitOpts{Phone: sess.PhoneNumber})
		} else {
			r.NextState = sm.StateAskSedation
		}

		return r, nil
	}
}

// GFR_NOT_ELIGIBLE (automático) — GFR < 30, bloquea contraste.
func gfrNotEligibleHandler() sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		return buildAutoCloseResult("Tu resultado de función renal no cumple con los requisitos para este examen con contraste.\n\nConsulta con tu médico tratante.").
			WithEvent("gfr_not_eligible", map[string]interface{}{
				"gfr_value": sess.GetContext("gfr_calculated"),
			}), nil
	}
}

// ASK_SEDATION (automático) — pregunta si requiere sedación.
// Si el CUPS no es sedatable, auto-chains to CHECK_EXISTING.
// Si el OCR ya detectó sedación en la orden, se auto-completa.
func askSedationHandler() sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		cupsCode := sess.GetContext("cups_code")

		// CUPS no sedatable → saltar SOLO si el OCR tampoco marcó sedación (simétrico al fix M5 de
		// contraste). Antes se salía solo por el prefijo del cups_code, descartando ocr_is_sedated=1:
		// una TAC pediátrica "bajo sedación" (879xxx, no 883) perdía el flag y NO se ruteaba a
		// SOPORTE SEDACIÓN (asunto 17); se agendaba en la agenda normal del estudio.
		if !isSedatable(cupsCode) && sess.GetContext("ocr_is_sedated") != "1" {
			return sm.NewResult(sm.StateCheckExisting).
				WithContext("is_sedated", "0").
				WithClearCtx("ocr_is_sedated").
				WithEvent("sedation_skipped", map[string]interface{}{"cups_code": cupsCode}), nil
		}

		// Si el OCR ya detectó sedación en la orden, auto-completar
		if sess.GetContext("ocr_is_sedated") == "1" {
			return sm.NewResult(sm.StateCheckExisting).
				WithContext("is_sedated", "1").
				WithClearCtx("ocr_is_sedated").
				WithText("Tu orden indica que el examen requiere *sedación*. Lo tendremos en cuenta.").
				WithEvent("sedation_auto_detected", map[string]interface{}{"cups_code": cupsCode}), nil
		}

		// First entry: show prompt
		if sess.GetContext("_prompted_sedation") == "" {
			return sm.NewResult(sess.CurrentState).
				WithContext("_prompted_sedation", "1").
				WithButtons(
					"¿Tu examen requiere *sedación*?\n\n(Esto lo indica tu médico, generalmente para niños o pacientes con claustrofobia)",
					sm.Button{Text: "Sí, con sedación", Payload: "sedated_yes"},
					sm.Button{Text: "No, sin sedación", Payload: "sedated_no"},
				), nil
		}

		result, selected := sm.ValidateButtonResponse(sess, msg, "sedated_yes", "sedated_no")
		if result != nil {
			result.Messages = []sm.OutboundMessage{&sm.ButtonMessage{
				Text: "¿Tu examen requiere *sedación*?\n\n(Esto lo indica tu médico, generalmente para niños o pacientes con claustrofobia)",
				Buttons: []sm.Button{
					{Text: "Sí, con sedación", Payload: "sedated_yes"},
					{Text: "No, sin sedación", Payload: "sedated_no"},
				},
			}}
			return result, nil
		}

		isSedated := "0"
		if selected == "sedated_yes" {
			isSedated = "1"
		}

		return sm.NewResult(sm.StateCheckExisting).
			WithContext("is_sedated", isSedated).
			WithClearCtx("_prompted_sedation").
			WithEvent("sedation_selected", map[string]interface{}{"sedated": selected == "sedated_yes"}), nil
	}
}

// CHECK_EXISTING (automático) — verifica si ya tiene cita futura para el mismo CUPS.
func checkExistingHandler(apptSvc *services.AppointmentService) sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		patientID := sess.GetContext("patient_id")
		cupsCode := sess.GetContext("cups_code")

		hasExisting, err := apptSvc.HasExistingAppointment(ctx, patientID, cupsCode)
		if err != nil {
			// Si falla, no bloquear
			return sm.NewResult(sm.StateCheckPriorConsult), nil
		}

		if hasExisting {
			observability.Emit(observability.TraceSession(sess.ID), "agendar", "already_has_appt",
				observability.EmitOpts{Phone: sess.PhoneNumber, Attrs: map[string]interface{}{"cups": cupsCode}})
			return sm.NewResult(sm.StateAppointmentExists).
				WithEvent("existing_appointment_found", map[string]interface{}{"cups_code": cupsCode}), nil
		}

		return sm.NewResult(sm.StateCheckPriorConsult), nil
	}
}

// APPOINTMENT_EXISTS (automático) — informa que ya tiene cita y va al menú.
func appointmentExistsHandler() sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		cupsName := sess.GetContext("cups_name")
		return buildAutoCloseResult(fmt.Sprintf("Ya tienes una cita pendiente para *%s*.", cupsName)).
			WithEvent("appointment_exists_blocked", nil), nil
	}
}

// CHECK_PRIOR_CONSULTATION (automático) — busca el médico de la última consulta previa
// y lo guarda como preferred_doctor para el agendamiento. No bloquea: si el paciente
// tiene una orden médica, ya tuvo la consulta con el especialista.
func checkPriorConsultHandler(apptSvc *services.AppointmentService) sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		cupsCode := sess.GetContext("cups_code")
		patientID := sess.GetContext("patient_id")

		consultCups := services.GetConsultCupsFor(cupsCode)
		if consultCups == nil {
			return sm.NewResult(sm.StateCheckMRCLimit), nil
		}

		if patientID == "" {
			return sm.NewResult(sm.StateCheckMRCLimit), nil
		}

		doctor, err := apptSvc.FindLastDoctorForCups(ctx, patientID, consultCups)
		if err != nil {
			slog.Warn("prior_consult_doctor_lookup_error", "cups_code", cupsCode, "patient_id", patientID, "error", err)
			return sm.NewResult(sm.StateCheckMRCLimit), nil
		}

		if doctor != "" {
			return sm.NewResult(sm.StateCheckMRCLimit).
				WithContext("preferred_doctor_doc", doctor), nil
		}

		return sm.NewResult(sm.StateCheckMRCLimit), nil
	}
}

// CHECK_MRC_LIMIT (automático) — marca flag para filtro mensual MRC en búsqueda de slots.
// Ya no bloquea aquí; el filtro se aplica en SEARCH_SLOTS via MonthFilter.
// Solo aplica a pacientes Sanitas MRC (contratos 5 y 6 en SIESA).
func checkMRCLimitHandler(apptSvc *services.AppointmentService) sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		cupsCode := sess.GetContext("cups_code")
		contract := sess.GetContext("patient_contract")

		if services.IsMRCPatient(contract) {
			if _, _, found := services.IsMRCGroupCups(cupsCode); found {
				return sm.NewResult(sm.StateCheckAgeRestriction).
					WithContext("mrc_limit_check", "1"), nil
			}
		}

		return sm.NewResult(sm.StateCheckAgeRestriction), nil
	}
}

// CHECK_AGE_RESTRICTION (automático) — registra que pasó todas las validaciones.
// Las restricciones de edad por doctor se aplican al filtrar slots (Fase 10).
func checkAgeRestrictionHandler() sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		return sm.NewResult(sm.StateSearchSlots).
			WithEvent("validations_complete", nil), nil
	}
}

// --- Special CUPS Routing ---

// Pregnancy ultrasound CUPS codes with their valid gestational week ranges.
// Format: [minWeeksTenths, maxWeeksTenths] (weeks × 10, so 13.6 → 136)
type gestationalRange struct {
	label string // human-readable range for messages
	min   int    // weeks × 10
	max   int    // weeks × 10
}

var pregnancyUltrasoundCups = map[string]gestationalRange{
	"881436": {label: "11 y 13 semanas + 6 días", min: 110, max: 136}, // Translucencia nucal
	"881437": {label: "18 y 24 semanas", min: 180, max: 240},          // Detalle anatómico
}

// Sleep study CUPS codes (polisomnografía / estudios del sueño)
var sleepStudyCups = map[string]bool{
	"891901": true,
	"891402": true,
	"891704": true,
	"891703": true,
}

// isPregnancyUltrasound checks if CUPS is a pregnancy ultrasound.
func isPregnancyUltrasound(cupsCode string) bool {
	_, ok := pregnancyUltrasoundCups[cupsCode]
	return ok
}

// isSleepStudy checks if CUPS requires routing to an agent.
func isSleepStudy(cupsCode string) bool {
	return sleepStudyCups[cupsCode]
}

// CHECK_SPECIAL_CUPS (automático) — verifica CUPS especiales antes de validaciones médicas.
func checkSpecialCupsHandler() sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		cupsCode := sess.GetContext("cups_code")

		// Pregnancy ultrasound → confirm gestational week range
		if _, ok := pregnancyUltrasoundCups[cupsCode]; ok {
			return sm.NewResult(sm.StateAskGestationalWeeks).
				WithEvent("special_cups_pregnancy_ultrasound", map[string]interface{}{"cups_code": cupsCode}), nil
		}

		// Sleep study → escalate to agent (requires special scheduling)
		if isSleepStudy(cupsCode) {
			observability.Emit(observability.TraceSession(sess.ID), "agendar", "special_escalated",
				observability.EmitOpts{Phone: sess.PhoneNumber, Reason: "sleep_study", Attrs: map[string]interface{}{"cups": cupsCode}})
			return sm.NewResult(sm.StateEscalateToAgent).
				WithText("Los *estudios del sueño* requieren una coordinación especial. Te comunicaremos con un agente para programar tu cita.").
				WithEvent("special_cups_sleep_study", map[string]interface{}{"cups_code": cupsCode}), nil
		}

		// Normal CUPS → proceed to contrast check
		return sm.NewResult(sm.StateAskContrasted), nil
	}
}

// ASK_GESTATIONAL_WEEKS (interactivo) — pide la semana de gestación exacta, valida el rango y
// calcula la fecha límite para mostrar solo slots dentro de la ventana clínica permitida.
// La semana se guarda en gestational_max_date (YYYY-MM-DD) = hoy + días hasta semana máxima.
func askGestationalWeeksHandler() sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		cupsCode := sess.GetContext("cups_code")
		gr := pregnancyUltrasoundCups[cupsCode]

		questionText := "¿En qué semana de gestación se encuentra actualmente? _(escribe el número, por ejemplo: *12* o *12.5*)_"

		// Primera llamada: mostrar pregunta y detenerse
		if sess.GetContext("_prompted_weeks") == "" {
			return sm.NewResult(sess.CurrentState).
				WithContext("_prompted_weeks", "1").
				WithText(questionText), nil
		}

		// Comandos del agente: weeks_yes / weeks_no (sin número de semana).
		// weeks_yes sin número no permite calcular maxDate → no se filtra por fecha, se continúa.
		isAgentInput := strings.HasPrefix(msg.ID, "agent-")
		if isAgentInput {
			switch strings.TrimSpace(msg.Text) {
			case "weeks_yes":
				sess.RetryCount = 0
				return sm.NewResult(sm.StateAskContrasted).
					WithClearCtx("_prompted_weeks").
					WithEvent("gestational_weeks_confirmed", map[string]interface{}{"cups_code": cupsCode, "range": gr.label}), nil
			case "weeks_no":
				sess.RetryCount = 0
				return buildAutoCloseResult(fmt.Sprintf(
					"Esta ecografía requiere estar entre las *%s* de gestación.\n\nCuando estés en ese rango, vuelve a contactarnos y con gusto te agendaremos.",
					gr.label,
				)).
					WithClearCtx("_prompted_weeks").
					WithEvent("gestational_weeks_out_of_range", map[string]interface{}{"cups_code": cupsCode, "range": gr.label}), nil
			}
		}

		// Parsear número de semanas (paciente o agente con número específico).
		weekStr := strings.TrimSpace(strings.Replace(msg.Text, ",", ".", 1))
		weeks, err := strconv.ParseFloat(weekStr, 64)
		if err != nil || weeks <= 0 {
			// Respuesta no reconocida: re-preguntar (máx 3 intentos antes de escalar).
			return sm.NewResult(sess.CurrentState).
				WithText(fmt.Sprintf("Por favor indica el número de semanas (ejemplo: *12* o *12.5*).\n\n%s", questionText)), nil
		}

		maxWeeks := float64(gr.max) / 10.0
		minWeeks := float64(gr.min) / 10.0

		// Si la paciente ya superó la semana máxima, ya no hay ventana posible → cerrar.
		daysToMax := int((maxWeeks - weeks) * 7)
		if daysToMax < 0 {
			sess.RetryCount = 0
			return buildAutoCloseResult(fmt.Sprintf(
				"Para esta ecografía es necesario que se realice entre las *%s* de gestación. Lamentablemente en semana %.1f ya no es posible realizarla.\n\nConsulta con tu médico las opciones disponibles.",
				gr.label, weeks,
			)).
				WithClearCtx("_prompted_weeks").
				WithEvent("gestational_weeks_past_range", map[string]interface{}{"cups_code": cupsCode, "range": gr.label, "weeks": weeks}), nil
		}

		// Calcular ventana de slots: [hoy + díasHastaMin, hoy + díasHastaMax].
		// Si la paciente ya pasó o está en la semana mínima, el inicio es hoy (sin MinDate).
		maxDate := time.Now().AddDate(0, 0, daysToMax)
		maxDateStr := maxDate.Format("2006-01-02")

		var minDateStr string
		daysToMin := int((minWeeks - weeks) * 7)
		if daysToMin > 0 {
			minDate := time.Now().AddDate(0, 0, daysToMin)
			minDateStr = minDate.Format("2006-01-02")
		}

		infoMsg := fmt.Sprintf(
			"Para esta ecografía es necesario que se realice entre las *%s* de gestación. Buscaremos los horarios disponibles en ese período.",
			gr.label,
		)
		sess.RetryCount = 0
		result := sm.NewResult(sm.StateAskContrasted).
			WithText(infoMsg).
			WithContext("gestational_max_date", maxDateStr).
			WithClearCtx("_prompted_weeks").
			WithEvent("gestational_weeks_confirmed", map[string]interface{}{
				"cups_code":   cupsCode,
				"range":       gr.label,
				"weeks":       weeks,
				"min_date":    minDateStr,
				"max_date":    maxDateStr,
				"days_to_min": daysToMin,
				"days_to_max": daysToMax,
			})
		if minDateStr != "" {
			result = result.WithContext("gestational_min_date", minDateStr)
		}
		return result, nil
	}
}
