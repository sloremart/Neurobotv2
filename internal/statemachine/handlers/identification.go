package handlers

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/observability"
	"github.com/neuro-bot/neuro-bot/internal/services"
	"github.com/neuro-bot/neuro-bot/internal/session"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
	"github.com/neuro-bot/neuro-bot/internal/statemachine/validators"
	"github.com/neuro-bot/neuro-bot/internal/utils"
)

// RegisterIdentificationHandlers registra ASK_DOCUMENT, PATIENT_LOOKUP, CONFIRM_IDENTITY
// y los nuevos estados de verificación de datos de contacto.
// docTypeAIHint arma el hint de recuperación IA con las 12 opciones REALES del catálogo (número →
// nombre), para que el LLM pueda mapear cualquier tipo dicho en palabras (ej. "registro civil" → 3),
// no solo unos ejemplos. El valor a inyectar es el número de la opción.
func docTypeAIHint() string {
	var b strings.Builder
	b.WriteString("El TIPO DE DOCUMENTO del paciente. Elige una opción; v = ÚNICAMENTE el dígito de la opción (1 al 12), jamás el nombre:\n")
	for i, d := range documentTypeCatalog {
		fmt.Fprintf(&b, "%d) %s\n", i+1, d.Label)
	}
	return b.String()
}

func RegisterIdentificationHandlers(m *sm.Machine, patientSvc *services.PatientService) {
	// Tipo de documento (15 tipos del catálogo SIESA) — se pregunta ANTES del número.
	// sis_paci no es único por num_id (mismo número con distinto tipo_id existe), así que la
	// búsqueda necesita tipo + número. El tipo también se reutiliza si el paciente no existe y
	// pasa a registro.
	m.Register(sm.StateAskDocumentType, askDocumentTypeHandler())
	// Opt-in de recuperación IA (piloto). El valor a inyectar es el número de opción (1-12);
	// regla de dominio: NO inferir el tipo de documento a partir del número (ver docs/RECUPERACION-IA.md §5).
	m.RegisterRecovery(sm.StateAskDocumentType, sm.HandlerConfig{
		AIRecovery:   true,
		AIInputHint:  docTypeAIHint(),
		TextValidate: func(s string) bool { return parseDocType(s) != "" },
		AIDomainBlock: func(input string) (bool, string, map[string]string) {
			if looksLikeDocNumber(input) {
				return true, "Parece que enviaste tu *número* de documento. Primero dime tu *tipo* de documento respondiendo el número de la opción (ej.: *1* = Cédula de Ciudadanía).", nil
			}
			return false, "", nil
		},
	})
	m.RegisterWithConfig(sm.StateAskDocument, sm.HandlerConfig{
		InputType:    sm.InputText,
		TextValidate: validators.Document,
		ErrorMsg:     "Por favor ingresa un número de documento válido (solo números, entre 5 y 15 dígitos).",
		Handler:      askDocumentHandler(),
	})
	m.Register(sm.StatePatientLookup, patientLookupHandler(patientSvc))
	m.Register(sm.StateConfirmIdentity, confirmIdentityHandler())

	// Verificación de datos de contacto
	m.Register(sm.StateShowContactInfo, showContactInfoHandler())
	m.RegisterWithConfig(sm.StateConfirmContactInfo, sm.HandlerConfig{
		InputType: sm.InputButton,
		Options:   []string{"contact_ok", "contact_update"},
		RetryPrompt: func(sess *session.Session, result *sm.StateResult) {
			phone := utils.FormatPhoneDisplay(sess.GetContext("patient_phone"))
			email := sess.GetContext("patient_email")
			if email == "" {
				email = "(no registrado)"
			}
			result.Messages = append(result.Messages, &sm.ButtonMessage{
				Text: fmt.Sprintf("Por favor selecciona una opcion.\n\nTus datos de contacto:\n\nCelular: %s\nEmail: %s\n\n¿Son correctos?", phone, email),
				Buttons: []sm.Button{
					{Text: "Sí, son correctos", Payload: "contact_ok"},
					{Text: "No, actualizar", Payload: "contact_update"},
				},
			})
		},
		Handler: confirmContactInfoHandler(patientSvc),
	})
	m.RegisterWithConfig(sm.StateAskUpdatePhone, sm.HandlerConfig{
		InputType:    sm.InputText,
		TextValidate: validators.ColombianPhone,
		ErrorMsg:     "Número no válido. Ingresa un celular colombiano de 10 dígitos (ej: 3001234567). Debe ser un número con WhatsApp.",
		Handler:      askUpdatePhoneHandler(),
	})
	m.Register(sm.StateAskUpdateEmail, askUpdateEmailHandler())
	m.Register(sm.StateUpdateContactInfo, updateContactInfoHandler(patientSvc))
}

// ASK_DOCUMENT_TYPE — menú de texto numerado (15 tipos del catálogo SIESA). Guarda el tipo
// (para la búsqueda y para reusarlo en el registro) y pasa a pedir el número.
func askDocumentTypeHandler() sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		input := strings.TrimSpace(msg.Text)
		// A: el paciente escribió su NÚMERO de documento en vez del tipo (confusión común con el prompt).
		// Asumir CC (la gran mayoría) y seguir DIRECTO al lookup con ese número, avisando — en vez de
		// rechazar y terminar escalando por reintentos. Si no fuera CC, el "no encontrado" lo encamina a
		// registro (donde elige el tipo correcto).
		if parseDocType(input) == "" && looksLikeDocNumber(input) {
			sess.RetryCount = 0
			return sm.NewResult(sm.StatePatientLookup).
				WithContext("patient_doc_type", "CC").
				WithContext("reg_document_type", "CC").
				WithContext("patient_doc", input).
				WithText("Asumimos *Cédula de Ciudadanía*. Buscando tu información…").
				WithEvent("doc_type_assumed_cc", map[string]interface{}{"doc": utils.MaskDocument(input)}), nil
		}
		retry := sm.ValidateWithRetry(sess, input,
			func(s string) bool { return parseDocType(s) != "" }, docTypeMenuText())
		if retry != nil {
			return retry, nil
		}
		sess.RetryCount = 0
		code := parseDocType(input)
		return sm.NewResult(sm.StateAskDocument).
			WithContext("patient_doc_type", code).
			WithContext("reg_document_type", code). // reusado por el flujo de registro si el paciente no existe
			WithText("Ahora ingresa tu *número de documento* (sin puntos ni espacios):").
			WithEvent("document_type_selected", map[string]interface{}{"type": code}), nil
	}
}

// ASK_DOCUMENT — solo lógica de negocio (validación declarativa en RegisterWithConfig).
func askDocumentHandler() sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		input := strings.TrimSpace(msg.Text)
		return sm.NewResult(sm.StatePatientLookup).
			WithContext("patient_doc", input).
			WithEvent("document_entered", map[string]interface{}{"doc_length": len(input)}), nil
	}
}

// PATIENT_LOOKUP (automático) — busca paciente en BD externa
func patientLookupHandler(patientSvc *services.PatientService) sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		doc := sess.GetContext("patient_doc")
		docType := sess.GetContext("patient_doc_type")

		patient, err := patientSvc.LookupByDocument(ctx, docType, doc)
		if err != nil {
			slog.Error("patient_lookup_failed", "doc_len", len(doc), "error", err)
			observability.Emit(observability.TraceSession(sess.ID), "identificacion", "lookup_failed",
				observability.EmitOpts{Phone: sess.PhoneNumber})
			return buildAutoCloseResult("Lo siento, hubo un problema al buscar tu información. Intenta más tarde.").
				WithEvent("patient_lookup_error", map[string]interface{}{"error": err.Error()}), nil
		}

		if patient == nil {
			observability.Emit(observability.TraceSession(sess.ID), "identificacion", "patient_lookup",
				observability.EmitOpts{Phone: sess.PhoneNumber, Reason: "not_found"})
			menuOption := sess.GetContext("menu_option")

			if menuOption == "agendar" {
				return sm.NewResult(sm.StateRegistrationStart).
					WithButtons(
						"No encontramos un paciente con ese documento. Para agendar una cita necesitas registrarte primero.\n\n¿Deseas registrarte?",
						sm.Button{Text: "Sí, registrarme", Payload: "register_yes"},
						sm.Button{Text: "No, gracias", Payload: "register_no"},
					).
					WithEvent("patient_not_found", map[string]interface{}{"doc": utils.MaskDocument(doc), "can_register": true}), nil
			}

			// Menú consultar → no puede consultar sin estar registrado
			return buildAutoCloseResult("No encontramos un paciente con el documento *"+doc+"*. Verifica que el número sea correcto.\n\nSi eres paciente nuevo, selecciona la opción *Agendar cita* para registrarte.").
				WithEvent("patient_not_found", map[string]interface{}{"doc": utils.MaskDocument(doc), "can_register": false}), nil
		}

		// Paciente encontrado → guardar datos en sesión
		observability.Emit(observability.TraceSession(sess.ID), "identificacion", "patient_lookup",
			observability.EmitOpts{Phone: sess.PhoneNumber, Reason: "found"})
		fullName := services.FormatFullName(patient)
		age := services.FormatAge(patient.BirthDate)

		return sm.NewResult(sm.StateConfirmIdentity).
			WithContext("patient_id", patient.ID).
			WithContext("patient_name", fullName).
			WithContext("patient_age", age).
			WithContext("patient_gender", patient.Gender).
			WithContext("patient_entity", patient.EntityCode).
			WithContext("patient_contract", patient.ContractCode).
			WithContext("patient_phone", patient.Phone).
			WithContext("patient_email", patient.Email).
			WithContext("patient_dep", patient.DepartmentCode).
			WithContext("patient_muni", patient.CityCode).
			WithContext("patient_muni_name", patient.CityName).
			WithButtons(
				fmt.Sprintf("Encontré este paciente:\n\n*%s*\nDocumento: %s\n\n¿Eres tú?", fullName, doc),
				sm.Button{Text: "Sí, soy yo", Payload: "identity_yes"},
				sm.Button{Text: "No, no soy yo", Payload: "identity_no"},
			).
			WithEvent("patient_found", map[string]interface{}{
				"patient_id": patient.ID,
				"name":       fullName,
			}), nil
	}
}

// CONFIRM_IDENTITY (interactivo) — confirmar si es el paciente correcto
func confirmIdentityHandler() sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		result, selected := sm.ValidateButtonResponse(sess, msg, "identity_yes", "identity_no")
		if result != nil {
			// L7: en el reintento por input inválido, re-renderizar los botones Sí/No (de lo contrario
			// el paciente solo recibe un texto sin botones). En la escalación por max-reintentos
			// (NextState != estado actual) se deja intacto el resultado.
			if result.NextState == sess.CurrentState {
				result.Messages = []sm.OutboundMessage{&sm.ButtonMessage{
					Text: fmt.Sprintf("Encontré este paciente:\n\n*%s*\nDocumento: %s\n\n¿Eres tú?",
						sess.GetContext("patient_name"), sess.GetContext("patient_doc")),
					Buttons: []sm.Button{
						{Text: "Sí, soy yo", Payload: "identity_yes"},
						{Text: "No, no soy yo", Payload: "identity_no"},
					},
				}}
			}
			return result, nil
		}

		switch selected {
		case "identity_yes":
			r := sm.NewResult(sm.StateShowContactInfo).
				WithEvent("patient_identified", map[string]interface{}{
					"patient_id": sess.GetContext("patient_id"),
				})

			// Guardar patient data en la sesión DB (campos directos)
			sess.PatientID = sess.GetContext("patient_id")
			sess.PatientDoc = sess.GetContext("patient_doc")
			sess.PatientName = sess.GetContext("patient_name")
			sess.PatientEntity = sess.GetContext("patient_entity")

			return r, nil

		case "identity_no":
			return sm.NewResult(sm.StateAskDocument).
				WithText("Entendido. Por favor ingresa tu número de documento correcto.").
				WithClearCtx("patient_id", "patient_name", "patient_age", "patient_gender", "patient_entity", "patient_contract", "patient_phone", "patient_email").
				WithEvent("patient_identity_rejected", nil), nil
		}

		return nil, fmt.Errorf("unreachable")
	}
}

// routeAfterContactInfo determina el siguiente estado según menu_option.
// Centraliza la lógica de routing que antes estaba en confirmIdentityHandler.
// Persiste la entidad seleccionada en BD externa (equivalente a GetPatientByDocumentJob en Laravel).
func routeAfterContactInfo(ctx context.Context, sess *session.Session, r *sm.StateResult, patientSvc *services.PatientService) {
	menuOption := sess.GetContext("menu_option")
	switch menuOption {
	case "consultar":
		r.NextState = sm.StateFetchAppointments
	case "agendar":
		// Bird V2: entity already selected before document entry → auto-update
		selectedEntity := sess.GetContext("selected_entity_code")
		if selectedEntity != "" {
			sess.PatientEntity = selectedEntity
			r.WithContext("patient_entity", selectedEntity)
			// Persistir entidad en BD externa
			patientID := sess.GetContext("patient_id")
			if patientSvc != nil && patientID != "" {
				if err := patientSvc.UpdateEntity(ctx, patientID, selectedEntity); err != nil {
					slog.Warn("update_entity_failed", "patient_id", patientID, "entity", selectedEntity, "error", err)
					observability.Emit(observability.TraceSession(sess.ID), "entidad", "entity_update_failed",
						observability.EmitOpts{Phone: sess.PhoneNumber})
				}
			}
		}

		// EPS contract resolution (régimen + Sanitas municipality).
		if isEPSWithMatrix(selectedEntity) {
			// SANITAS depends on the residence municipality → confirm/edit it first.
			if selectedEntity == entitySanitas {
				// K2: si el paciente Sanitas NO tiene municipio registrado, no se puede distinguir
				// MRC de Evento → en vez de ofrecer "confirmar" algo vacío (que caería a Evento en
				// silencio), se le OBLIGA a capturarlo antes de continuar. finalizeSanitasMunicipality
				// lo persiste en sis_paci y resuelve el contrato correcto.
				if sess.GetContext("patient_muni") == "" {
					observability.Emit(observability.TraceSession(sess.ID), "entidad", "sanitas_muni_forced",
						observability.EmitOpts{Phone: sess.PhoneNumber})
					r.NextState = sm.StateConfirmMunicipality
					r.WithText("Necesitamos registrar tu *municipio de residencia* para continuar.\n\nEscríbelo junto con el departamento (ej: *Acacías - Meta*):")
					return
				}
				muniName := sess.GetContext("patient_muni_name")
				if muniName == "" {
					muniName = "el registrado"
				}
				r.NextState = sm.StateConfirmMunicipality
				r.WithButtons(
					fmt.Sprintf("Para asignar tu contrato SANITAS necesitamos confirmar tu municipio de residencia.\n\nTenemos registrado: *%s*\n\n¿Es correcto?", muniName),
					sm.Button{Text: "Sí, es correcto", Payload: "muni_ok"},
					sm.Button{Text: "No, cambiar", Payload: "muni_change"},
				)
				return
			}
			// Other EPS only need the régimen.
			applyEPSContract(ctx, sess, r, patientSvc, sess.GetContext("patient_id"), selectedEntity, sess.GetContext("eps_regimen"), "", "")
		}

		// PARTICULAR: clear any stored contract so lookupContract resolves the
		// particular contract (PART02 → its active contrato) on the cita.
		if selectedEntity == particularEntityCode {
			r.WithContext("patient_contract", "")
		}

		r.NextState = sm.StateAskMedicalOrder
	default:
		r.NextState = sm.StateTerminated
	}
}

// applyEPSContract resolves the EPS contract from (entity, régimen, municipality),
// stores it for the booking (patient_contract) and persists it to the patient
// (sis_paci.contrato) — Option B: the contract stays tied to the patient and the
// cita. No-op when the entity is not part of the matrix or the contract is empty.
func applyEPSContract(ctx context.Context, sess *session.Session, r *sm.StateResult, patientSvc *services.PatientService, patientID, entity, regimen, depCode, muniCode string) {
	contract := resolveEPSContract(entity, regimen, depCode, muniCode)
	if contract == "" {
		return
	}
	// K2: si es Sanitas y la geo (depto/municipio) viene vacía, no se puede distinguir MRC de
	// Evento → resolveEPSContract cae a Evento (4/7) en SILENCIO, lo que mal-cobraría a un paciente
	// MRC y lo dejaría fuera del tope mensual MRC. Hoy es inalcanzable (0 casos en BD), pero por ser
	// financiero se deja traza para revisarlo manualmente si llegara a pasar.
	if entity == entitySanitas && (depCode == "" || muniCode == "") {
		slog.Warn(
			"sanitas_contract_empty_geo",
			"patient_id", patientID,
			"regimen", regimen,
			"dep", depCode,
			"muni", muniCode,
			"contract", contract,
			"note", "geo vacía: posible MRC asignado como Evento; verificar contrato manualmente",
		)
	}
	slog.Info(
		"eps_contract_resolved",
		"patient_id", patientID,
		"entity", entity,
		"regimen", regimen,
		"dep", depCode,
		"muni", muniCode,
		"contract", contract,
	)
	r.WithContext("patient_contract", contract)
	observability.Emit(observability.TraceSession(sess.ID), "entidad", "contract_resolved",
		observability.EmitOpts{Phone: sess.PhoneNumber, Attrs: map[string]interface{}{"contrato": contract}})
	if patientSvc != nil && patientID != "" {
		// Best-effort: la cita usa el contrato del contexto en memoria, pero si el UPDATE a
		// sis_paci falla queremos traza (Ley 1581 / diagnóstico de contención con la UI SIESA).
		if err := patientSvc.UpdateContract(ctx, patientID, contract); err != nil {
			slog.Warn("update_contract_failed", "patient_id", patientID, "contract", contract, "error", err)
		}
	}
}

// SHOW_CONTACT_INFO (automático) — evalúa datos de contacto y decide siguiente paso
func showContactInfoHandler() sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		rawPhone := sess.GetContext("patient_phone")
		rawEmail := sess.GetContext("patient_email")

		parsedPhone := utils.ParseColombianPhone(rawPhone)
		hasValidPhone := parsedPhone != ""
		hasEmail := rawEmail != "" && strings.ToLower(rawEmail) != "null"

		// Si el celular no es válido → pedir obligatoriamente
		if !hasValidPhone {
			return sm.NewResult(sm.StateAskUpdatePhone).
				WithText("No tenemos un celular válido registrado para ti.\n\nNecesitamos tu número de celular con WhatsApp para enviarte recordatorios y confirmaciones de tus citas.\n\nIngresa tu número de celular (ej: 3001234567):").
				WithEvent("contact_info_missing", map[string]interface{}{"missing": "phone"}), nil
		}

		// Si tiene celular pero no email → pedir email
		if !hasEmail {
			return sm.NewResult(sm.StateAskUpdateEmail).
				WithContext("contact_new_phone", parsedPhone).
				WithText(fmt.Sprintf("Tu celular registrado es: *%s*\n\nAhora necesitamos tu correo electrónico para enviarte información importante.\n\nIngresa tu email o responde *NA* si no tienes:", utils.FormatPhoneDisplay(rawPhone))).
				WithEvent("contact_info_missing", map[string]interface{}{"missing": "email"}), nil
		}

		// Ambos datos completos → mostrar y pedir confirmación
		phoneDisplay := utils.FormatPhoneDisplay(rawPhone)

		return sm.NewResult(sm.StateConfirmContactInfo).
			WithButtons(
				fmt.Sprintf("Tus datos de contacto registrados son:\n\nCelular: *%s*\nEmail: *%s*\n\nEs importante que estén actualizados ya que los usamos para enviarte recordatorios y confirmaciones de citas por WhatsApp.\n\n¿Son correctos?", phoneDisplay, rawEmail),
				sm.Button{Text: "Sí, son correctos", Payload: "contact_ok"},
				sm.Button{Text: "No, actualizar", Payload: "contact_update"},
			).
			WithEvent("contact_info_shown", map[string]interface{}{
				"phone": phoneDisplay,
				"email": rawEmail,
			}), nil
	}
}

// CONFIRM_CONTACT_INFO (interactivo) — confirmar o actualizar datos de contacto
func confirmContactInfoHandler(patientSvc *services.PatientService) sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		result, selected := sm.ValidateButtonResponse(sess, msg, "contact_ok", "contact_update")
		if result != nil {
			// L7: re-renderizar los botones en el reintento por input inválido (no en la escalación).
			if result.NextState == sess.CurrentState {
				phoneDisplay := utils.FormatPhoneDisplay(sess.GetContext("patient_phone"))
				result.Messages = []sm.OutboundMessage{&sm.ButtonMessage{
					Text: fmt.Sprintf("Tus datos de contacto registrados son:\n\nCelular: *%s*\nEmail: *%s*\n\n¿Son correctos?",
						phoneDisplay, sess.GetContext("patient_email")),
					Buttons: []sm.Button{
						{Text: "Sí, son correctos", Payload: "contact_ok"},
						{Text: "No, actualizar", Payload: "contact_update"},
					},
				}}
			}
			return result, nil
		}

		switch selected {
		case "contact_ok":
			r := sm.NewResult("").
				WithEvent("contact_info_confirmed", nil)
			routeAfterContactInfo(ctx, sess, r, patientSvc)
			return r, nil

		case "contact_update":
			return sm.NewResult(sm.StateAskUpdatePhone).
				WithText("Ingresa tu número de celular (debe ser un número con WhatsApp):\n\nEj: 3001234567").
				WithEvent("contact_update_requested", nil), nil
		}

		return nil, fmt.Errorf("unreachable")
	}
}

// ASK_UPDATE_PHONE (interactivo) — capturar nuevo celular
func askUpdatePhoneHandler() sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		input := strings.TrimSpace(msg.Text)
		parsed := utils.ParseColombianPhone(input)

		return sm.NewResult(sm.StateAskUpdateEmail).
			WithContext("contact_new_phone", parsed).
			WithText("Ingresa tu correo electrónico o responde *NA* si no tienes:").
			WithEvent("contact_phone_updated", map[string]interface{}{"phone": parsed}), nil
	}
}

// ASK_UPDATE_EMAIL (interactivo) — capturar nuevo email
func askUpdateEmailHandler() sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		input := strings.TrimSpace(msg.Text)
		lower := strings.ToLower(input)

		// Aceptar NA y variantes
		naResponses := map[string]bool{
			"no tengo": true, "no": true, "na": true, "n/a": true, "-": true, "ninguno": true,
		}
		if naResponses[lower] {
			return sm.NewResult(sm.StateUpdateContactInfo).
				WithContext("contact_new_email", "").
				WithEvent("contact_email_skipped", nil), nil
		}

		// Validar formato de email
		retryResult := sm.ValidateWithRetry(sess, lower, validators.Email, "Email no válido. Ingresa un correo como ejemplo@correo.com o responde *NA* si no tienes.")
		if retryResult != nil {
			return retryResult, nil
		}

		sess.RetryCount = 0
		return sm.NewResult(sm.StateUpdateContactInfo).
			WithContext("contact_new_email", lower).
			WithEvent("contact_email_updated", map[string]interface{}{"email": lower}), nil
	}
}

// UPDATE_CONTACT_INFO (automático) — actualizar datos en BD externa y continuar flujo
func updateContactInfoHandler(patientSvc *services.PatientService) sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		patientID := sess.GetContext("patient_id")
		newPhone := sess.GetContext("contact_new_phone")
		newEmail := sess.GetContext("contact_new_email")

		err := patientSvc.UpdateContactInfo(ctx, patientID, newPhone, newEmail)
		if err != nil {
			// No bloquear el flujo por error de actualización
			r := sm.NewResult("").
				WithText("No pudimos actualizar tus datos en este momento, pero puedes continuar.").
				WithEvent("contact_update_error", map[string]interface{}{"error": err.Error()})
			routeAfterContactInfo(ctx, sess, r, patientSvc)
			return r, nil
		}

		observability.Emit(observability.TraceSession(sess.ID), "identificacion", "contact_updated",
			observability.EmitOpts{Phone: sess.PhoneNumber})

		// Actualizar contexto de sesión con los nuevos valores
		r := sm.NewResult("").
			WithContext("patient_phone", newPhone).
			WithContext("patient_email", newEmail).
			WithText("Datos de contacto actualizados correctamente.").
			WithEvent("contact_info_updated", map[string]interface{}{
				"phone": newPhone,
				"email": newEmail,
			})
		routeAfterContactInfo(ctx, sess, r, patientSvc)
		return r, nil
	}
}
