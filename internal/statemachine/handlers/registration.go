package handlers

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/observability"
	"github.com/neuro-bot/neuro-bot/internal/repository"
	"github.com/neuro-bot/neuro-bot/internal/services"
	"github.com/neuro-bot/neuro-bot/internal/session"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
	"github.com/neuro-bot/neuro-bot/internal/statemachine/validators"
	"github.com/neuro-bot/neuro-bot/internal/utils"
)

// RegisterRegistrationHandlers registra todos los handlers del flujo de registro de paciente nuevo.
func RegisterRegistrationHandlers(
	m *sm.Machine,
	patientSvc *services.PatientService,
	municipalityRepo repository.MunicipalityRepository,
) {
	m.RegisterWithConfig(sm.StateRegistrationStart, sm.HandlerConfig{
		InputType: sm.InputButton,
		Options:   []string{"register_yes", "register_no"},
		RetryPrompt: func(sess *session.Session, result *sm.StateResult) {
			result.Messages = append(result.Messages, &sm.ButtonMessage{
				Text: "¿Deseas registrarte?",
				Buttons: []sm.Button{
					{Text: "Sí, registrarme", Payload: "register_yes"},
					{Text: "No, gracias", Payload: "register_no"},
				},
			})
		},
		Handler: registrationStartHandler(),
	})
	// Tipo de documento: 12 tipos del catálogo SIESA sis_tipo_documento (de los 15 totales se
	// excluyen AS/MS/SI "sin identificación"). WhatsApp limita las listas interactivas a 10
	// filas, por eso se ofrece como menú de texto numerado.
	m.Register(sm.StateRegDocumentType, withCorrectionRedirect(regDocumentTypeHandler()))
	m.Register(sm.StateRegFirstSurname, withCorrectionRedirect(regFieldHandler("reg_first_surname", "Por favor escribe tu primer apellido (solo letras, sin números, ni símbolos ni espacios).", validateName, sm.StateRegSecondSurname, "Si tienes *segundo apellido*, escríbelo. Si no, responde *NA*:")))
	m.Register(sm.StateRegSecondSurname, regOptionalFieldHandler("reg_second_surname", "Si tienes segundo apellido, escríbelo. Si no, responde *NA*:", sm.StateRegFirstName, "Por favor escribe tu *primer nombre* (solo letras, sin números ni símbolos):"))
	m.Register(sm.StateRegFirstName, withCorrectionRedirect(regFieldHandler("reg_first_name", "Por favor escribe tu primer nombre (solo letras, sin números, ni símbolos ni espacios).", validateName, sm.StateRegSecondName, "Si tienes *segundo nombre*, escríbelo. Si no, responde *NA*:")))
	m.Register(sm.StateRegSecondName, regOptionalFieldHandler("reg_second_name", "Si tienes segundo nombre, escríbelo. Si no, responde *NA*:", sm.StateRegBirthDate, "Ingresa tu *fecha de nacimiento* en formato *AAAA-MM-DD* (ejemplo: 1992-04-17):"))
	m.Register(sm.StateRegBirthDate, withCorrectionRedirect(regBirthDateHandler()))
	m.RegisterWithConfig(sm.StateRegGender, sm.HandlerConfig{
		InputType: sm.InputButton,
		Options:   []string{"M", "F"},
		RetryPrompt: func(sess *session.Session, result *sm.StateResult) {
			result.Messages = append(result.Messages, &sm.ButtonMessage{
				Text:    "Selecciona tu género:",
				Buttons: []sm.Button{{Text: "Masculino", Payload: "M"}, {Text: "Femenino", Payload: "F"}},
			})
		},
		Handler: regGenderHandler(),
	})
	m.RegisterWithConfig(sm.StateRegBloodType, sm.HandlerConfig{
		InputType: sm.InputButton,
		Options:   bloodTypeValues(),
		RetryPrompt: func(sess *session.Session, result *sm.StateResult) {
			result.Messages = append(result.Messages, &sm.ListMessage{
				Body: "Selecciona tu grupo sanguíneo (RH):", Title: "Seleccionar",
				Sections: []sm.ListSection{{Title: "Grupo sanguíneo", Rows: bloodTypeListRows()}},
			})
		},
		Handler: withCorrectionRedirect(regBloodTypeHandler()),
	})
	m.RegisterWithConfig(sm.StateRegMaritalStatus, sm.HandlerConfig{
		InputType: sm.InputButton,
		Options:   []string{"1", "2", "3", "4", "5"},
		RetryPrompt: func(sess *session.Session, result *sm.StateResult) {
			result.Messages = append(result.Messages, &sm.ListMessage{
				Body: "Selecciona tu estado civil:", Title: "Seleccionar",
				Sections: []sm.ListSection{{Title: "Estado civil", Rows: maritalStatusListRows()}},
			})
		},
		Handler: withCorrectionRedirect(regMaritalStatusHandler()),
	})
	m.Register(sm.StateRegAddress, withCorrectionRedirect(regFieldHandler("reg_address", "Escribe tu dirección (calle y número).", validateNotEmpty, sm.StateRegBarrio, "¿En qué *barrio* vives? Escribe el nombre (ej: La Esperanza) o responde *NA* si no lo sabes:")))
	m.Register(sm.StateRegPhone, withCorrectionRedirect(regPhoneHandler()))
	m.Register(sm.StateRegPhone2, regOptionalPhoneHandler())
	m.Register(sm.StateRegEmail, withCorrectionRedirect(regEmailHandler()))
	m.Register(sm.StateRegMunicipality, regMunicipalityHandler(municipalityRepo))
	m.Register(sm.StateRegBarrio, regBarrioHandler(municipalityRepo))
	m.Register(sm.StateConfirmMunicipality, confirmMunicipalityHandler(municipalityRepo, patientSvc))
	m.RegisterWithConfig(sm.StateRegZone, sm.HandlerConfig{
		InputType: sm.InputButton,
		Options:   []string{"U", "R"},
		RetryPrompt: func(sess *session.Session, result *sm.StateResult) {
			result.Messages = append(result.Messages, &sm.ButtonMessage{
				Text:    "Selecciona tu zona:",
				Buttons: []sm.Button{{Text: "Urbana", Payload: "U"}, {Text: "Rural", Payload: "R"}},
			})
		},
		Handler: regZoneHandler(),
	})
	m.RegisterWithConfig(sm.StateRegUserType, sm.HandlerConfig{
		InputType: sm.InputButton,
		Options:   userTypePayloads(),
		RetryPrompt: func(sess *session.Session, result *sm.StateResult) {
			result.Messages = append(result.Messages, &sm.ListMessage{
				Body: "Selecciona tu tipo de usuario:", Title: "Tipo de usuario",
				Sections: []sm.ListSection{{
					Title: "Tipo de usuario",
					Rows:  userTypeListRows(),
				}},
			})
		},
		Handler: withCorrectionRedirect(regUserTypeHandler()),
	})
	m.RegisterWithConfig(sm.StateRegAffiliationType, sm.HandlerConfig{
		InputType: sm.InputButton,
		Options:   affiliationTypeValues(),
		RetryPrompt: func(sess *session.Session, result *sm.StateResult) {
			result.Messages = append(result.Messages, &sm.ListMessage{
				Body: "Selecciona tu tipo de afiliación:", Title: "Tipo de afiliación",
				Sections: []sm.ListSection{{Title: "Tipo de afiliación", Rows: affiliationTypeListRows()}},
			})
		},
		Handler: regAffiliationTypeHandler(),
	})
	m.RegisterWithConfig(sm.StateRegSelectCorrection, sm.HandlerConfig{
		InputType: sm.InputButton,
		Options:   correctionPayloads(),
		RetryPrompt: func(sess *session.Session, result *sm.StateResult) {
			result.Messages = append(result.Messages, buildCorrectionList())
		},
		Handler: regSelectCorrectionHandler(),
	})
	m.RegisterWithConfig(sm.StateConfirmRegistration, sm.HandlerConfig{
		InputType: sm.InputButton,
		Options:   []string{"reg_confirm", "reg_correct"},
		RetryPrompt: func(sess *session.Session, result *sm.StateResult) {
			result.Messages = append(result.Messages, &sm.ButtonMessage{
				Text: buildRegistrationSummary(sess),
				Buttons: []sm.Button{
					{Text: "✅ Sí, confirmar", Payload: "reg_confirm"},
					{Text: "✏️ Corregir datos", Payload: "reg_correct"},
				},
			})
		},
		Handler: confirmRegistrationHandler(),
	})
	m.Register(sm.StateCreatePatient, createPatientHandler(patientSvc))
}

// --- Validaciones reutilizables (delegadas al paquete validators) ---

var validateName = validators.Name

func validateNotEmpty(s string) bool {
	return len(strings.TrimSpace(s)) > 0
}

var noResponses = map[string]bool{
	"no tengo": true, "no": true, "ninguno": true, "ninguna": true, "n/a": true, "na": true, "-": true,
}

// --- Handlers genéricos ---

// regFieldHandler crea un handler para campos de texto con validación.
// El handler anterior ya envió el prompt; este solo procesa la respuesta.
// nextPrompt es el texto que se envía al usuario como indicación del siguiente campo.
func regFieldHandler(ctxKey, prompt string, validate func(string) bool, nextState, nextPrompt string) sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		input := strings.TrimSpace(msg.Text)

		// Reject internal trigger texts (e.g. __resume__) so they are
		// never stored as real patient data.
		if sm.IsReservedKeyword(input) {
			input = ""
		}

		retryResult := sm.ValidateWithRetry(sess, input, validate, "Respuesta no válida. "+prompt)
		if retryResult != nil {
			return retryResult, nil
		}

		r := sm.NewResult(nextState).
			WithContext(ctxKey, strings.ToUpper(input))
		if nextPrompt != "" {
			r.WithText(nextPrompt)
		}
		return r, nil
	}
}

// regOptionalFieldHandler crea un handler para campos opcionales.
// nextPrompt es el texto que se envía como indicación del siguiente campo.
func regOptionalFieldHandler(ctxKey, prompt, nextState, nextPrompt string) sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		input := strings.TrimSpace(msg.Text)

		if noResponses[strings.ToLower(input)] {
			r := sm.NewResult(nextState).
				WithContext(ctxKey, "")
			if nextPrompt != "" {
				r.WithText(nextPrompt)
			}
			return r, nil
		}

		if !validateName(input) {
			retryResult := sm.ValidateWithRetry(sess, input, validateName, "Respuesta no válida. "+prompt)
			if retryResult != nil {
				return retryResult, nil
			}
		}

		r := sm.NewResult(nextState).
			WithContext(ctxKey, strings.ToUpper(input))
		if nextPrompt != "" {
			r.WithText(nextPrompt)
		}
		return r, nil
	}
}

// --- Handlers especializados ---

// REGISTRATION_START — solo lógica de negocio (validación declarativa en RegisterWithConfig).
func registrationStartHandler() sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		selected := sm.ValidatedPayload(ctx)
		switch selected {
		case "register_yes":
			// El tipo de documento (reg_document_type) y el número (patient_doc) ya se pidieron
			// antes de la búsqueda, así que el registro arranca directo en el primer apellido.
			observability.Emit(observability.TraceSession(sess.ID), "registro", "registration_started",
				observability.EmitOpts{Phone: sess.PhoneNumber})
			r := sm.NewResult(sm.StateRegFirstSurname).
				WithText("¡Perfecto! Vamos a registrarte.\n\nPor favor escribe tu *primer apellido* (solo letras, sin números ni símbolos):").
				WithEvent("registration_started", nil)

			// Entity and client type already selected before registration
			if entityCode := sess.GetContext("selected_entity_code"); entityCode != "" {
				r.WithContext("reg_entity", entityCode)
				r.WithContext("patient_entity", entityCode)
			}
			if entityName := sess.GetContext("selected_entity_name"); entityName != "" {
				r.WithContext("reg_entity_name", entityName)
			}
			if clientType := sess.GetContext("client_type"); clientType != "" {
				r.WithContext("reg_client_type", clientType)
			}

			return r, nil
		case "register_no":
			observability.Emit(observability.TraceSession(sess.ID), "registro", "registration_abandoned",
				observability.EmitOpts{Phone: sess.PhoneNumber})
			return buildAutoCloseResult("Entendido. Si necesitas algo más, estamos aquí para ayudarte.").
				WithEvent("registration_declined", nil), nil
		}
		return nil, fmt.Errorf("unreachable")
	}
}

// documentTypeCatalog mirrors SIESA's sis_tipo_documento (abreviatura → descripción).
// The Code is what gets stored in sis_paci.tipo_id. Ordered by expected frequency.
var documentTypeCatalog = []struct{ Code, Label string }{
	{"CC", "Cédula de Ciudadanía"},
	{"TI", "Tarjeta de Identidad"},
	{"RC", "Registro Civil"},
	{"CE", "Cédula de Extranjería"},
	{"PA", "Pasaporte"},
	{"PT", "Permiso por Protección Temporal"},
	{"PE", "Permiso Especial"},
	{"CN", "Certificado de Nacido Vivo"},
	{"NUI", "Número Único de Identificación"},
	{"NIT", "Número de Identificación Tributaria"},
	{"SV", "Salvoconducto"},
	{"DE", "Documento Extranjero"},
}

// docTypeMenuText renders the numbered text menu (WhatsApp lists cap at 10 rows, so the 12
// document types are offered as text). The "sin identificación" types (AS/MS/SI) are excluded:
// a patient must have a real identification document to register.
func docTypeMenuText() string {
	var b strings.Builder
	// B: mensaje anti-confusión. La gente leía "responde con el número" y escribía su NÚMERO de cédula
	// (8-10 dígitos) en vez de la opción 1-12 → input inválido → reintentos → escalación evitable.
	b.WriteString("¿Cuál es tu *tipo* de documento? Responde con el *número de la opción* de la lista (1-12), *no* con tu número de cédula.\n")
	b.WriteString("👉 La mayoría: responde *1* (Cédula de Ciudadanía).\n")
	for i, d := range documentTypeCatalog {
		b.WriteString(fmt.Sprintf("%d. %s - %s\n", i+1, d.Code, d.Label))
	}
	return strings.TrimRight(b.String(), "\n")
}

// looksLikeDocNumber detecta que, en el paso de TIPO de documento, el paciente escribió su NÚMERO de
// documento (confusión común con el prompt). Es un número de ≥5 dígitos (cédula/TI tienen 6-10):
// inequívocamente NO es una opción de menú (1-12). Permite asumir CC y seguir en vez de escalar.
func looksLikeDocNumber(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) < 5 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// parseDocType resolves a user reply (a 1..N number where N=len(documentTypeCatalog)=12, or the code itself) to a catalog code,
// or "" when invalid.
func parseDocType(input string) string {
	s := strings.ToUpper(strings.TrimSpace(input))
	if n, err := strconv.Atoi(s); err == nil && n >= 1 && n <= len(documentTypeCatalog) {
		return documentTypeCatalog[n-1].Code
	}
	for _, d := range documentTypeCatalog {
		if d.Code == s {
			return d.Code
		}
	}
	return ""
}

// REG_DOCUMENT_TYPE — menú de texto numerado (12 tipos del catálogo SIESA).
func regDocumentTypeHandler() sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		input := strings.TrimSpace(msg.Text)
		// A: el paciente escribió su NÚMERO de documento en el paso del tipo (confusión común). Asumir CC
		// y continuar (el número ya se capturó en identificación; aquí solo falta el tipo) en vez de
		// rechazar y terminar escalando por reintentos.
		if parseDocType(input) == "" && looksLikeDocNumber(input) {
			sess.RetryCount = 0
			return sm.NewResult(sm.StateRegFirstSurname).
				WithContext("reg_document_type", "CC").
				WithText("Asumimos *Cédula de Ciudadanía* (si es otro tipo, podrás corregirlo después).\n\nAhora escribe tu *primer apellido* (solo letras, sin números ni símbolos):").
				WithEvent("doc_type_assumed_cc", map[string]interface{}{"flow": "registro"}), nil
		}
		retry := sm.ValidateWithRetry(sess, input,
			func(s string) bool { return parseDocType(s) != "" }, docTypeMenuText())
		if retry != nil {
			return retry, nil
		}
		sess.RetryCount = 0
		return sm.NewResult(sm.StateRegFirstSurname).
			WithContext("reg_document_type", parseDocType(input)).
			WithText("Por favor escribe tu *primer apellido* (solo letras, sin números ni símbolos):"), nil
	}
}

// REG_BIRTH_DATE (interactivo) — fecha de nacimiento AAAA-MM-DD
func regBirthDateHandler() sm.StateHandler {
	formats := []string{"2006-01-02", "2006/01/02"}

	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		input := strings.TrimSpace(msg.Text)

		var parsedDate time.Time
		var parseErr error
		for _, format := range formats {
			parsedDate, parseErr = time.Parse(format, input)
			if parseErr == nil {
				break
			}
		}

		if parseErr != nil {
			retryResult := sm.ValidateWithRetry(sess, "", func(string) bool { return false },
				"Formato de fecha no válido. Ingresa tu fecha de nacimiento en formato *AAAA-MM-DD* (ejemplo: 1992-04-17).")
			return retryResult, nil
		}

		now := time.Now()
		if parsedDate.After(now) {
			return sm.NewResult(sess.CurrentState).
				WithText("La fecha no puede ser en el futuro. Ingresa tu fecha de nacimiento real en formato *AAAA-MM-DD*:"), nil
		}
		if parsedDate.Year() < 1900 {
			return sm.NewResult(sess.CurrentState).
				WithText("Fecha no válida. Ingresa tu fecha de nacimiento en formato *AAAA-MM-DD*:"), nil
		}

		age := services.CalculateAge(parsedDate)
		sess.RetryCount = 0

		// Lugar de nacimiento: SIESA no tiene columna para ello (eliminado del flujo).
		// Pasamos directo a género.
		return sm.NewResult(sm.StateRegGender).
			WithContext("reg_birth_date", parsedDate.Format("2006-01-02")).
			WithContext("patient_age", fmt.Sprintf("%d", age)).
			WithButtons(
				"Selecciona tu *género*:",
				sm.Button{Text: "Masculino", Payload: "M"},
				sm.Button{Text: "Femenino", Payload: "F"},
			), nil
	}
}

// REG_GENDER — solo lógica de negocio (validación declarativa en RegisterWithConfig).
func regGenderHandler() sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		selected := sm.ValidatedPayload(ctx)
		return sm.NewResult(sm.StateRegBloodType).
			WithContext("reg_gender", selected).
			WithContext("patient_gender", selected).
			WithList("Selecciona tu *grupo sanguíneo (RH)*:", "Seleccionar",
				sm.ListSection{
					Title: "Grupo sanguíneo",
					Rows:  bloodTypeListRows(),
				}), nil
	}
}

// REG_BLOOD_TYPE — solo lógica de negocio (validación declarativa en RegisterWithConfig).
// Guarda el valor exacto del catálogo sis_tiposangre (ej. "O+") en reg_blood_type.
func regBloodTypeHandler() sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		selected := sm.ValidatedPayload(ctx)
		return sm.NewResult(sm.StateRegMaritalStatus).
			WithContext("reg_blood_type", selected).
			WithList("Selecciona tu *estado civil*:", "Seleccionar",
				sm.ListSection{
					Title: "Estado civil",
					Rows:  maritalStatusListRows(),
				}), nil
	}
}

// REG_MARITAL_STATUS — solo lógica de negocio (validación declarativa en RegisterWithConfig).
func regMaritalStatusHandler() sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		selected := sm.ValidatedPayload(ctx)
		return sm.NewResult(sm.StateRegPhone).
			WithContext("reg_marital_status", selected).
			WithText("Ingresa tu *celular principal* preferiblemente con WhatsApp (ej: 3001234567):"), nil
	}
}

// REG_PHONE (interactivo) — teléfono principal colombiano
func regPhoneHandler() sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		input := strings.TrimSpace(msg.Text)

		parsed := utils.ParseColombianPhone(input)
		if parsed == "" {
			retryResult := sm.ValidateWithRetry(sess, "", func(string) bool { return false },
				"Número no válido. Ingresa tu celular principal preferiblemente con WhatsApp (ej: 3001234567).")
			return retryResult, nil
		}

		sess.RetryCount = 0
		return sm.NewResult(sm.StateRegPhone2).
			WithContext("reg_phone", parsed).
			WithContext("patient_phone", parsed).
			WithText("Ingresa un número de celular secundario (ej: 3001234567) o responde *NA*:"), nil
	}
}

// REG_PHONE2 (interactivo) — teléfono secundario opcional
func regOptionalPhoneHandler() sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		input := strings.TrimSpace(msg.Text)

		if noResponses[strings.ToLower(input)] {
			return sm.NewResult(sm.StateRegEmail).
				WithContext("reg_phone2", "").
				WithText("Indica tu correo electrónico o responde *NA*:"), nil
		}

		parsed := utils.ParseColombianPhone(input)
		if parsed == "" {
			// No es un teléfono válido, pero es campo opcional, guardar vacío
			return sm.NewResult(sm.StateRegEmail).
				WithContext("reg_phone2", "").
				WithText("Indica tu correo electrónico o responde *NA*:"), nil
		}

		return sm.NewResult(sm.StateRegEmail).
			WithContext("reg_phone2", parsed).
			WithText("Indica tu correo electrónico o responde *NA*:"), nil
	}
}

// REG_EMAIL (interactivo) — email o "no tengo"
func regEmailHandler() sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		input := strings.TrimSpace(msg.Text)
		lower := strings.ToLower(input)

		if noResponses[lower] {
			return sm.NewResult(sm.StateRegUserType).
				WithContext("reg_email", "").
				WithList("Selecciona tu *tipo de usuario*:", "Tipo de usuario",
					sm.ListSection{Title: "Tipo de usuario", Rows: userTypeListRows()}), nil
		}

		if !validators.Email(lower) {
			retryResult := sm.ValidateWithRetry(sess, "", func(string) bool { return false },
				"Email no válido. Indica tu correo electrónico o responde *NA*:")
			return retryResult, nil
		}

		sess.RetryCount = 0
		return sm.NewResult(sm.StateRegUserType).
			WithContext("reg_email", lower).
			WithList("Selecciona tu *tipo de usuario*:", "Tipo de usuario",
				sm.ListSection{Title: "Tipo de usuario", Rows: userTypeListRows()}), nil
	}
}

// REG_MUNICIPALITY (interactivo) — búsqueda fuzzy de municipios
func regMunicipalityHandler(municipalityRepo repository.MunicipalityRepository) sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		// Selección desde lista (postback con código de municipio)
		if msg.IsPostback {
			sess.RetryCount = 0
			// payload es "depCod-muniCod" (ej: "50-606") para evitar colisiones
			parts := strings.SplitN(msg.PostbackPayload, "-", 2)
			muniCode := msg.PostbackPayload
			deptCode := ""
			if len(parts) == 2 {
				deptCode = parts[0]
				muniCode = parts[1]
			}
			displayName := sess.GetContext("muni_" + msg.PostbackPayload)
			return sm.NewResult(sm.StateRegZone).
				WithContext("reg_municipality", muniCode).
				WithContext("reg_department", deptCode).
				WithContext("reg_municipality_name", displayName).
				WithButtons(
					fmt.Sprintf("Municipio seleccionado: *%s*\n\nSelecciona tu *zona*:", displayName),
					sm.Button{Text: "Urbana", Payload: "U"},
					sm.Button{Text: "Rural", Payload: "R"},
				), nil
		}

		input := strings.TrimSpace(msg.Text)
		if input == "" {
			return sm.NewResult(sess.CurrentState).
				WithText("Nombre completo de tu municipio y departamento de residencia. Ejemplo: Villavicencio - Meta:"), nil
		}

		// Atajo: si el paciente escribe "0" usa Villavicencio por defecto
		if input == "0" {
			sess.RetryCount = 0
			return sm.NewResult(sm.StateRegZone).
				WithContext("reg_municipality", "001").
				WithContext("reg_department", "50").
				WithContext("reg_municipality_name", "VILLAVICENCIO - META").
				WithButtons(
					"Municipio seleccionado: *VILLAVICENCIO - META*\n\nSelecciona tu *zona*:",
					sm.Button{Text: "Urbana", Payload: "U"},
					sm.Button{Text: "Rural", Payload: "R"},
				), nil
		}

		results, err := municipalityRepo.Search(ctx, input)
		if err != nil {
			return sm.NewResult(sess.CurrentState).
				WithText("No pudimos buscar el municipio en este momento. Intenta de nuevo escribiendo el nombre de tu municipio:"), nil
		}

		if len(results) == 0 {
			sess.RetryCount++
			return sm.NewResult(sess.CurrentState).
				WithText("No encontré ese municipio. Escribe el municipio y departamento (ej: *Villavicencio - Meta*, *Acacías - Meta*) o escribe *0* para usar Villavicencio por defecto:"), nil
		}

		outcome, errResult := sm.ValidateSearchCount(sess, len(results), 5,
			"No encontré municipios con ese nombre. Intenta con otro nombre o sé más específico:",
			"Encontré demasiados resultados. Sé más específico (ejemplo: escribe \"Villavicencio\" en lugar de \"Villa\"):")
		if errResult != nil {
			return errResult, nil
		}

		switch outcome {
		case sm.SearchExact:
			sess.RetryCount = 0
			muniDisplay := fmt.Sprintf("%s - %s", results[0].MunicipalityName, results[0].DepartmentName)
			return sm.NewResult(sm.StateRegZone).
				WithContext("reg_municipality", results[0].MunicipalityCode).
				WithContext("reg_department", results[0].DepartmentCode).
				WithContext("reg_municipality_name", muniDisplay).
				WithButtons(
					fmt.Sprintf("Municipio seleccionado: *%s*\n\nSelecciona tu *zona*:", muniDisplay),
					sm.Button{Text: "Urbana", Payload: "U"},
					sm.Button{Text: "Rural", Payload: "R"},
				), nil
		default: // SearchMultiple
			rows := make([]sm.ListRow, len(results))
			r := sm.NewResult(sess.CurrentState)
			for i, res := range results {
				// ID compuesto "depCod-muniCod" para evitar colisiones cuando dos
				// municipios tienen el mismo codigo en departamentos distintos
				compositeID := res.DepartmentCode + "-" + res.MunicipalityCode
				rows[i] = sm.ListRow{
					ID:          compositeID,
					Title:       res.MunicipalityName,
					Description: res.DepartmentName,
				}
				r.WithContext("muni_"+compositeID, fmt.Sprintf("%s - %s", res.MunicipalityName, res.DepartmentName))
			}
			return r.WithList("Selecciona tu municipio:", "Municipios",
				sm.ListSection{Title: "Resultados", Rows: rows}), nil
		}
	}
}

// REG_ZONE — solo lógica de negocio (validación declarativa en RegisterWithConfig).
func regZoneHandler() sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		selected := sm.ValidatedPayload(ctx)
		return sm.NewResult(sm.StateRegAddress).
			WithContext("reg_zone", selected).
			WithText("Escribe tu *dirección* (calle y número):"), nil
	}
}

// REG_USER_TYPE — solo lógica de negocio (validación declarativa en RegisterWithConfig).
func regUserTypeHandler() sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		selected := sm.ValidatedPayload(ctx)
		return sm.NewResult(sm.StateRegAffiliationType).
			WithContext("reg_user_type", selected).
			WithList("Selecciona tu *tipo de afiliación*:", "Tipo de afiliación",
				sm.ListSection{Title: "Tipo de afiliación", Rows: affiliationTypeListRows()}), nil
	}
}

// userTypePayloads returns the valid payloads for the 9 user type options.
// DB column TipoUsuario is double — stores numeric values 1-9.
func userTypePayloads() []string {
	return []string{"1", "2", "3", "4", "5", "6", "7", "8", "9"}
}

// userTypeListRows returns the 9 user type options for WhatsApp list.
// IDs are numeric (1-9) matching the DB column TipoUsuario (double).
func userTypeListRows() []sm.ListRow {
	return []sm.ListRow{
		{ID: "1", Title: "Contributivo", Description: "Aporta al sistema con cotización laboral"},
		{ID: "2", Title: "Subsidiado", Description: "Recibe subsidio del Estado"},
		{ID: "3", Title: "Vinculado", Description: "Accede a servicios sin afiliación formal"},
		{ID: "4", Title: "Particular", Description: "Paga directamente por los servicios"},
		{ID: "5", Title: "Otro", Description: "No encaja en ninguna categoría anterior"},
		{ID: "6", Title: "Despl. Contributivo", Description: "Desplazado que aporta al régimen contributivo"},
		{ID: "7", Title: "Despl. Subsidiado", Description: "Desplazado que recibe subsidio del Estado"},
		{ID: "8", Title: "Despl. No asegurado", Description: "Desplazado no afiliado al sistema"},
		{ID: "9", Title: "Especial", Description: "Régimen especial (FF.MM., Ecopetrol, etc.)"},
	}
}

// REG_AFFILIATION_TYPE — solo lógica de negocio (validación declarativa en RegisterWithConfig).
func regAffiliationTypeHandler() sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		selected := sm.ValidatedPayload(ctx)
		// Inicia el grupo de dirección: municipio → zona → dirección → barrio.
		return sm.NewResult(sm.StateRegMunicipality).
			WithContext("reg_affiliation_type", selected).
			WithText("Escribe tu *municipio y departamento de residencia* (ej.: Villavicencio - Meta):"), nil
	}
}

// CONFIRM_REGISTRATION — solo lógica de negocio (validación declarativa en RegisterWithConfig).
func confirmRegistrationHandler() sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		selected := sm.ValidatedPayload(ctx)
		switch selected {
		case "reg_confirm":
			return sm.NewResult(sm.StateCreatePatient).
				WithEvent("registration_confirmed", nil), nil
		case "reg_correct":
			r := sm.NewResult(sm.StateRegSelectCorrection)
			r.Messages = append(r.Messages, buildCorrectionList())
			return r.WithEvent("registration_select_correction", nil), nil
		}
		return nil, fmt.Errorf("unreachable")
	}
}

// CONFIRM_MUNICIPALITY (interactivo) — SANITAS: confirma o edita el municipio de
// residencia para resolver el contrato (MRC vs Evento) antes de agendar.
func confirmMunicipalityHandler(municipalityRepo repository.MunicipalityRepository, patientSvc *services.PatientService) sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		if msg.IsPostback {
			switch msg.PostbackPayload {
			case "muni_ok":
				return finalizeSanitasMunicipality(ctx, sess, patientSvc,
					sess.GetContext("patient_dep"), sess.GetContext("patient_muni")), nil
			case "muni_change":
				return sm.NewResult(sm.StateConfirmMunicipality).
					WithText("Escribe el nombre de tu municipio y departamento de residencia (ej: *Acacías - Meta*):"), nil
			default:
				// Selección desde la lista de desambiguación: payload "depCod-muniCod".
				parts := strings.SplitN(msg.PostbackPayload, "-", 2)
				if len(parts) == 2 {
					return finalizeSanitasMunicipality(ctx, sess, patientSvc, parts[0], parts[1]), nil
				}
			}
		}

		input := strings.TrimSpace(msg.Text)
		if input == "" {
			return sm.NewResult(sm.StateConfirmMunicipality).
				WithText("Escribe tu municipio y departamento de residencia (ej: *Acacías - Meta*):"), nil
		}

		results, err := municipalityRepo.Search(ctx, input)
		if err != nil {
			return sm.NewResult(sm.StateConfirmMunicipality).
				WithText("No pudimos buscar el municipio en este momento. Intenta de nuevo:"), nil
		}
		if len(results) == 0 {
			sess.RetryCount++
			return sm.NewResult(sm.StateConfirmMunicipality).
				WithText("No encontré ese municipio. Escribe el municipio y departamento (ej: *Acacías - Meta*):"), nil
		}

		outcome, errResult := sm.ValidateSearchCount(sess, len(results), 5,
			"No encontré municipios con ese nombre. Intenta con otro nombre:",
			"Encontré demasiados resultados. Sé más específico con el departamento (ej: *Acacías - Meta*):")
		if errResult != nil {
			return errResult, nil
		}

		if outcome == sm.SearchExact {
			sess.RetryCount = 0
			return finalizeSanitasMunicipality(ctx, sess, patientSvc,
				results[0].DepartmentCode, results[0].MunicipalityCode), nil
		}

		// SearchMultiple → lista para desambiguar
		rows := make([]sm.ListRow, len(results))
		r := sm.NewResult(sm.StateConfirmMunicipality)
		for i, res := range results {
			compositeID := res.DepartmentCode + "-" + res.MunicipalityCode
			rows[i] = sm.ListRow{
				ID:          compositeID,
				Title:       res.MunicipalityName,
				Description: res.DepartmentName,
			}
		}
		return r.WithList("Selecciona tu municipio:", "Municipios",
			sm.ListSection{Title: "Resultados", Rows: rows}), nil
	}
}

// finalizeSanitasMunicipality resuelve y persiste el contrato SANITAS según el
// municipio confirmado/editado y continúa hacia la orden médica. Si el municipio
// cambió respecto al guardado, también lo persiste en el paciente (sis_paci).
func finalizeSanitasMunicipality(ctx context.Context, sess *session.Session, patientSvc *services.PatientService, depCode, muniCode string) *sm.StateResult {
	r := sm.NewResult(sm.StateAskMedicalOrder)
	patientID := sess.GetContext("patient_id")
	applyEPSContract(ctx, sess, r, patientSvc, patientID, entitySanitas, sess.GetContext("eps_regimen"), depCode, muniCode)

	// Persistir municipio/departamento si el paciente los cambió.
	changed := depCode != sess.GetContext("patient_dep") || muniCode != sess.GetContext("patient_muni")
	if changed && patientSvc != nil && patientID != "" && depCode != "" && muniCode != "" {
		// Best-effort con traza si falla el UPDATE a sis_paci (observabilidad / Ley 1581).
		if err := patientSvc.UpdateMunicipality(ctx, patientID, depCode, muniCode); err != nil {
			slog.Warn("update_municipality_failed", "patient_id", patientID, "dep", depCode, "muni", muniCode, "error", err)
		}
		r.WithContext("patient_dep", depCode).
			WithContext("patient_muni", muniCode)
	}
	return r
}

// REG_BARRIO (interactivo) — búsqueda de barrio en sis_barrios, acotada al municipio.
// Guarda sis_barrios.codigo en reg_barrio. Es el último dato antes del resumen.
func regBarrioHandler(municipalityRepo repository.MunicipalityRepository) sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		dep := sess.GetContext("reg_department")
		muni := sess.GetContext("reg_municipality")

		// Selección desde la lista de desambiguación (postback con código de barrio).
		if msg.IsPostback {
			sess.RetryCount = 0
			code := msg.PostbackPayload
			return finishRegistrationWithBarrio(sess, code, sess.GetContext("barrio_"+code)), nil
		}

		input := strings.TrimSpace(msg.Text)
		// Atajo: "NA" (o equivalentes) omite el barrio (queda sin dato). No se usa "0"
		// porque el interceptor global de menú lo captura antes de llegar aquí.
		if noResponses[strings.ToLower(input)] {
			sess.RetryCount = 0
			return finishRegistrationWithBarrio(sess, "", ""), nil
		}
		if input == "" {
			return sm.NewResult(sm.StateRegBarrio).
				WithText("Escribe el nombre de tu *barrio* (ej: La Esperanza) o responde *NA* si no lo sabes:"), nil
		}

		results, err := municipalityRepo.SearchBarrios(ctx, input, dep, muni)
		if err != nil {
			return sm.NewResult(sm.StateRegBarrio).
				WithText("No pudimos buscar el barrio en este momento. Intenta de nuevo:"), nil
		}
		if len(results) == 0 {
			sess.RetryCount++
			return sm.NewResult(sm.StateRegBarrio).
				WithText("No encontré ese barrio en tu municipio. Escribe el nombre de otra forma o responde *NA* si no lo sabes:"), nil
		}

		outcome, errResult := sm.ValidateSearchCount(sess, len(results), 9,
			"No encontré barrios con ese nombre. Intenta con otro:",
			"Encontré demasiados resultados. Sé más específico con el nombre del barrio:")
		if errResult != nil {
			return errResult, nil
		}

		if outcome == sm.SearchExact {
			sess.RetryCount = 0
			return finishRegistrationWithBarrio(sess, results[0].Code, results[0].Name), nil
		}

		rows := make([]sm.ListRow, len(results))
		r := sm.NewResult(sm.StateRegBarrio)
		for i, b := range results {
			rows[i] = sm.ListRow{ID: b.Code, Title: b.Name}
			r.WithContext("barrio_"+b.Code, b.Name)
		}
		return r.WithList("Selecciona tu barrio:", "Barrios",
			sm.ListSection{Title: "Resultados", Rows: rows}), nil
	}
}

// finishRegistrationWithBarrio guarda el barrio y muestra el resumen para confirmar.
func finishRegistrationWithBarrio(sess *session.Session, code, name string) *sm.StateResult {
	sess.SetContext("reg_barrio", code)
	sess.SetContext("reg_barrio_name", name)
	return sm.NewResult(sm.StateConfirmRegistration).
		WithContext("reg_barrio", code).
		WithContext("reg_barrio_name", name).
		WithButtons(
			buildRegistrationSummary(sess),
			sm.Button{Text: "✅ Sí, confirmar", Payload: "reg_confirm"},
			sm.Button{Text: "✏️ Corregir datos", Payload: "reg_correct"},
		)
}

// CREATE_PATIENT (automático) — crea paciente en BD externa
func createPatientHandler(patientSvc *services.PatientService) sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		birthDate, _ := time.Parse("2006-01-02", sess.GetContext("reg_birth_date"))

		// Entity comes from pre-registration context (entity_management.go)
		entityCode := sess.GetContext("reg_entity")
		if entityCode == "" {
			entityCode = sess.GetContext("selected_entity_code")
		}

		input := domain.CreatePatientInput{
			DocumentType:    sess.GetContext("reg_document_type"),
			DocumentNumber:  sess.GetContext("patient_doc"),
			FirstName:       sess.GetContext("reg_first_name"),
			SecondName:      sess.GetContext("reg_second_name"),
			FirstSurname:    sess.GetContext("reg_first_surname"),
			SecondSurname:   sess.GetContext("reg_second_surname"),
			BirthDate:       birthDate,
			Gender:          sess.GetContext("reg_gender"),
			Phone:           sess.GetContext("reg_phone"),
			Phone2:          sess.GetContext("reg_phone2"),
			Email:           sess.GetContext("reg_email"),
			Address:         sess.GetContext("reg_address"),
			DepartmentCode:  sess.GetContext("reg_department"),
			CityCode:        sess.GetContext("reg_municipality"),
			Zone:            sess.GetContext("reg_zone"),
			EntityCode:      entityCode,
			AffiliationType: sess.GetContext("reg_affiliation_type"),
			UserType:        sess.GetContext("reg_user_type"),
			MaritalStatus:   sess.GetContext("reg_marital_status"),
			BloodType:       sess.GetContext("reg_blood_type"),
			Barrio:          sess.GetContext("reg_barrio"),
			CountryCode:     "170",
		}

		patientID, err := patientSvc.Create(ctx, input)
		if err != nil {
			slog.Error(
				"patient_create_failed",
				"doc", utils.MaskDocument(input.DocumentNumber),
				"doc_type", input.DocumentType,
				"entity", input.EntityCode,
				"error", err.Error(),
			)
			observability.Emit(observability.TraceSession(sess.ID), "registro", "patient_create_failed",
				observability.EmitOpts{Phone: sess.PhoneNumber})
			return buildAutoCloseResult("Lo siento, hubo un error al crear tu registro. Por favor intenta más tarde o contacta a un agente.").
				WithEvent("registration_failed", map[string]interface{}{"error": err.Error()}), nil
		}

		// Construir nombre completo para la sesión
		nameParts := []string{}
		for _, p := range []string{
			sess.GetContext("reg_first_name"),
			sess.GetContext("reg_second_name"),
			sess.GetContext("reg_first_surname"),
			sess.GetContext("reg_second_surname"),
		} {
			if strings.TrimSpace(p) != "" {
				nameParts = append(nameParts, p)
			}
		}
		fullName := strings.Join(nameParts, " ")

		observability.Emit(observability.TraceSession(sess.ID), "registro", "patient_created",
			observability.EmitOpts{Phone: sess.PhoneNumber})

		// Guardar datos del paciente en sesión
		sess.PatientID = patientID
		sess.PatientDoc = sess.GetContext("patient_doc")
		sess.PatientName = fullName
		sess.PatientEntity = entityCode

		r := sm.NewResult(sm.StateAskMedicalOrder).
			WithContext("patient_id", patientID).
			WithContext("patient_name", fullName).
			WithText(fmt.Sprintf("✅ *¡Registro exitoso!*\n\nBienvenido/a *%s*. Ahora procedamos a agendar tu cita.", fullName)).
			WithEvent("registration_success", map[string]interface{}{"patient_id": patientID})

		// EPS contract resolution: the patient just entered régimen and municipality
		// during registration, so resolve and persist the contract directly.
		if isEPSWithMatrix(entityCode) {
			applyEPSContract(ctx, sess, r, patientSvc, patientID, entityCode,
				sess.GetContext("eps_regimen"),
				sess.GetContext("reg_department"),
				sess.GetContext("reg_municipality"))
		}

		return r, nil
	}
}

// --- Helpers ---

func buildRegistrationSummary(sess *session.Session) string {
	entityDisplay := sess.GetContext("reg_entity_name")
	if entityDisplay == "" {
		entityDisplay = sess.GetContext("reg_entity")
	}

	return fmt.Sprintf(
		"*Resumen de tu registro:*\n\n"+
			"*Datos personales*\n"+
			"Documento: %s %s\n"+
			"Nombre: %s %s %s %s\n"+
			"Nacimiento: %s (Edad: %s)\n"+
			"Género: %s\n"+
			"RH: %s\n"+
			"Estado civil: %s\n"+
			"Teléfono: %s\n"+
			"Email: %s\n\n"+
			"*Afiliación*\n"+
			"Entidad: %s\n"+
			"Tipo usuario: %s\n"+
			"Afiliación: %s\n\n"+
			"*Dirección*\n"+
			"Municipio: %s\n"+
			"Zona: %s\n"+
			"Dirección: %s\n"+
			"Barrio: %s\n\n"+
			"¿Los datos son correctos?",
		sess.GetContext("reg_document_type"), sess.GetContext("patient_doc"),
		sess.GetContext("reg_first_name"), sess.GetContext("reg_second_name"),
		sess.GetContext("reg_first_surname"), sess.GetContext("reg_second_surname"),
		sess.GetContext("reg_birth_date"), sess.GetContext("patient_age"),
		formatGender(sess.GetContext("reg_gender")),
		formatOptional(sess.GetContext("reg_blood_type")),
		formatMaritalStatus(sess.GetContext("reg_marital_status")),
		sess.GetContext("reg_phone"),
		formatOptional(sess.GetContext("reg_email")),
		entityDisplay,
		formatUserType(sess.GetContext("reg_user_type")),
		formatAffiliation(sess.GetContext("reg_affiliation_type")),
		formatMunicipality(sess.GetContext("reg_municipality"), sess.GetContext("reg_municipality_name")),
		formatZone(sess.GetContext("reg_zone")),
		sess.GetContext("reg_address"),
		formatOptional(sess.GetContext("reg_barrio_name")),
	)
}

func formatZone(z string) string {
	switch z {
	case "U":
		return "Urbana"
	case "R":
		return "Rural"
	default:
		return z
	}
}

func formatGender(g string) string {
	switch g {
	case "M":
		return "Masculino"
	case "F":
		return "Femenino"
	default:
		return g
	}
}

func formatMunicipality(code, name string) string {
	if name != "" {
		return fmt.Sprintf("%s (%s)", code, name)
	}
	return code
}

func formatOptional(s string) string {
	if s == "" {
		return "No tiene"
	}
	return s
}

// bloodTypeValues returns the exact catalog values (sis_tiposangre.valor) stored in
// sis_paci.tipo_sangre. Used as both list-row IDs and valid payloads.
func bloodTypeValues() []string {
	return []string{"O+", "O-", "A+", "A-", "B+", "B-", "AB+", "AB-", "No Aplica"}
}

// bloodTypeListRows returns the blood-type (RH) options from the sis_tiposangre catalog.
func bloodTypeListRows() []sm.ListRow {
	values := bloodTypeValues()
	rows := make([]sm.ListRow, len(values))
	for i, v := range values {
		rows[i] = sm.ListRow{ID: v, Title: v}
	}
	return rows
}

// maritalStatusListRows returns the 6 marital status options (numeric IDs for DB column EstadoCivil int).
func maritalStatusListRows() []sm.ListRow {
	return []sm.ListRow{
		{ID: "1", Title: "Soltero/a"},
		{ID: "2", Title: "Casado/a"},
		{ID: "3", Title: "Viudo/a"},
		{ID: "4", Title: "Unión libre"},
		{ID: "5", Title: "Divorciado/a"},
	}
}

// maritalStatusLabels maps numeric IDs to display labels.
var maritalStatusLabels = map[string]string{
	"1": "Soltero/a",
	"2": "Casado/a",
	"3": "Viudo/a",
	"4": "Unión libre",
	"5": "Divorciado/a",
}

func formatMaritalStatus(id string) string {
	if label, ok := maritalStatusLabels[id]; ok {
		return label
	}
	return id
}

// affiliationLabels maps TipoAfiliado catalog IDs to display labels.
var affiliationLabels = map[string]string{
	"1": "Cotizante",
	"2": "Beneficiario",
	"3": "Sustituto de pensión",
	"4": "Pensionado",
}

// affiliationTypeValues returns the valid payloads (TipoAfiliado catalog IDs).
func affiliationTypeValues() []string {
	return []string{"1", "2", "3", "4"}
}

// affiliationTypeListRows returns the 4 affiliation options from the TipoAfiliado catalog.
func affiliationTypeListRows() []sm.ListRow {
	return []sm.ListRow{
		{ID: "1", Title: "Cotizante"},
		{ID: "2", Title: "Beneficiario"},
		{ID: "3", Title: "Sustituto de pensión"},
		{ID: "4", Title: "Pensionado"},
	}
}

func formatAffiliation(id string) string {
	if label, ok := affiliationLabels[id]; ok {
		return label
	}
	return id
}

// userTypeLabels maps numeric user type IDs to display labels.
var userTypeLabels = map[string]string{
	"1": "Contributivo",
	"2": "Subsidiado",
	"3": "Vinculado",
	"4": "Particular",
	"5": "Otro",
	"6": "Despl. Contributivo",
	"7": "Despl. Subsidiado",
	"8": "Despl. No asegurado",
	"9": "Especial",
}

func formatUserType(id string) string {
	if label, ok := userTypeLabels[id]; ok {
		return label
	}
	return id
}

// --- Corrección selectiva de campos ---

// withCorrectionRedirect wraps a registration handler to redirect back to CONFIRM_REGISTRATION
// after successful correction of a single field. Passes through unchanged when reg_correction_mode
// is not set or when validation fails (handler stays in same state).
func withCorrectionRedirect(handler sm.StateHandler) sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		result, err := handler(ctx, sess, msg)
		if err != nil {
			return result, err
		}

		// Not in correction mode → pass through.
		if sess.GetContext("reg_correction_mode") != "true" {
			return result, nil
		}

		// Validation failed (stayed in same state) → pass through for retry.
		if result.NextState == sess.CurrentState {
			return result, nil
		}

		// Success in correction mode: apply context updates to session
		// so buildRegistrationSummary sees the new values.
		for k, v := range result.UpdateCtx {
			sess.SetContext(k, v)
		}

		// Build redirect result with updated summary.
		redirect := sm.NewResult(sm.StateConfirmRegistration)
		redirect.UpdateCtx = result.UpdateCtx
		redirect.WithClearCtx("reg_correction_mode")
		redirect.Events = result.Events
		redirect.WithText("Dato actualizado.").
			WithButtons(
				buildRegistrationSummary(sess),
				sm.Button{Text: "✅ Sí, confirmar", Payload: "reg_confirm"},
				sm.Button{Text: "✏️ Corregir datos", Payload: "reg_correct"},
			)

		return redirect, nil
	}
}

// correctionField defines a correctable field with its target state and prompt builder.
type correctionField struct {
	ID     string
	Title  string
	State  string
	Prompt func() sm.OutboundMessage // nil = full restart (no correction mode)
}

// correctionFields defines the 9 correctable fields + restart option (10 total for WhatsApp limit).
func correctionFields() []correctionField {
	return []correctionField{
		{
			ID: "corr_first_name", Title: "Primer nombre", State: sm.StateRegFirstName,
			Prompt: func() sm.OutboundMessage {
				return &sm.TextMessage{Text: "Por favor escribe tu primer nombre (solo letras, sin números, ni símbolos ni espacios)."}
			},
		},
		{
			ID: "corr_first_surname", Title: "Primer apellido", State: sm.StateRegFirstSurname,
			Prompt: func() sm.OutboundMessage {
				return &sm.TextMessage{Text: "Por favor escribe tu primer apellido (solo letras, sin números, ni símbolos ni espacios)."}
			},
		},
		{
			ID: "corr_birth_date", Title: "Fecha de nacimiento", State: sm.StateRegBirthDate,
			Prompt: func() sm.OutboundMessage {
				return &sm.TextMessage{Text: "Ingresa tu fecha de nacimiento en formato *AAAA-MM-DD* (ejemplo: 1992-04-17):"}
			},
		},
		{
			ID: "corr_address", Title: "Dirección", State: sm.StateRegAddress,
			Prompt: func() sm.OutboundMessage {
				return &sm.TextMessage{Text: "Escribe tu dirección completa (calle, número, barrio):"}
			},
		},
		{
			ID: "corr_phone", Title: "Teléfono", State: sm.StateRegPhone,
			Prompt: func() sm.OutboundMessage {
				return &sm.TextMessage{Text: "Ingresa tu celular principal preferiblemente con WhatsApp (ej: 3001234567):"}
			},
		},
		{
			ID: "corr_email", Title: "Email", State: sm.StateRegEmail,
			Prompt: func() sm.OutboundMessage {
				return &sm.TextMessage{Text: "Indica tu correo electrónico o responde *NA*:"}
			},
		},
		{
			ID: "corr_document_type", Title: "Tipo de documento", State: sm.StateRegDocumentType,
			Prompt: func() sm.OutboundMessage {
				return &sm.TextMessage{Text: docTypeMenuText()}
			},
		},
		{
			ID: "corr_marital_status", Title: "Estado civil", State: sm.StateRegMaritalStatus,
			Prompt: func() sm.OutboundMessage {
				return &sm.ListMessage{
					Body: "Selecciona tu *estado civil*:", Title: "Seleccionar",
					Sections: []sm.ListSection{{Title: "Estado civil", Rows: maritalStatusListRows()}},
				}
			},
		},
		{
			ID: "corr_user_type", Title: "Tipo de usuario", State: sm.StateRegUserType,
			Prompt: func() sm.OutboundMessage {
				return &sm.ListMessage{
					Body: "Selecciona tu *tipo de usuario*:", Title: "Tipo de usuario",
					Sections: []sm.ListSection{{Title: "Tipo de usuario", Rows: userTypeListRows()}},
				}
			},
		},
		{ID: "corr_restart", Title: "Empezar de nuevo", State: sm.StateRegDocumentType, Prompt: nil},
	}
}

// correctionPayloads returns valid postback payloads for the correction list.
func correctionPayloads() []string {
	fields := correctionFields()
	payloads := make([]string, len(fields))
	for i, f := range fields {
		payloads[i] = f.ID
	}
	return payloads
}

// buildCorrectionList builds the WhatsApp list message for field selection during correction.
func buildCorrectionList() *sm.ListMessage {
	fields := correctionFields()
	rows := make([]sm.ListRow, len(fields))
	for i, f := range fields {
		rows[i] = sm.ListRow{ID: f.ID, Title: f.Title}
	}
	return &sm.ListMessage{
		Body: "¿Qué dato deseas corregir?", Title: "Seleccionar campo",
		Sections: []sm.ListSection{{Title: "Campos", Rows: rows}},
	}
}

// regSelectCorrectionHandler handles the field selection for correction.
func regSelectCorrectionHandler() sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		selected := sm.ValidatedPayload(ctx)

		for _, field := range correctionFields() {
			if field.ID != selected {
				continue
			}

			// "Empezar de nuevo" = full restart without correction mode
			if field.Prompt == nil {
				return sm.NewResult(field.State).
					WithText("Vamos a corregir tus datos. Comencemos de nuevo.\n\n"+docTypeMenuText()).
					WithClearCtx("reg_correction_mode").
					WithEvent("registration_restart", nil), nil
			}

			// Normal correction: set mode and transition to target field with prompt
			r := sm.NewResult(field.State).
				WithContext("reg_correction_mode", "true")
			r.Messages = append(r.Messages, field.Prompt())
			return r, nil
		}

		return nil, fmt.Errorf("unreachable: invalid correction field %q", selected)
	}
}
