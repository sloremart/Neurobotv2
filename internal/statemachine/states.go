package statemachine

import "github.com/neuro-bot/neuro-bot/internal/session"

// === Saludo e Identificación ===
const (
	StateCheckBusinessHours = "CHECK_BUSINESS_HOURS"
	StateGreeting           = "GREETING"
	StateMainMenu           = "MAIN_MENU"
	StateAskDocumentType    = "ASK_DOCUMENT_TYPE"
	StateAskDocument        = "ASK_DOCUMENT"
	StatePatientLookup      = "PATIENT_LOOKUP"
	StateConfirmIdentity    = "CONFIRM_IDENTITY"
	StateShowContactInfo    = "SHOW_CONTACT_INFO"
	StateConfirmContactInfo = "CONFIRM_CONTACT_INFO"
	StateAskUpdatePhone     = "ASK_UPDATE_PHONE"
	StateAskUpdateEmail     = "ASK_UPDATE_EMAIL"
	StateUpdateContactInfo  = "UPDATE_CONTACT_INFO"
	StateShowResults        = "SHOW_RESULTS"
	StateShowLocations      = "SHOW_LOCATIONS"
	StateShowHelp           = "SHOW_HELP"
	// Aplicación de medicamentos: valida contrato, consulta listado, recopila datos clínicos y agenda.
	StateMedicationCheckSanitas    = "MEDICATION_CHECK_SANITAS"
	StateMedicationCheckExcel      = "MEDICATION_CHECK_EXCEL"
	StateMedicationAskLastDate     = "MEDICATION_ASK_LAST_DATE"
	StateMedicationAskDose         = "MEDICATION_ASK_DOSE"
	StateMedicationAskFrequency    = "MEDICATION_ASK_FREQUENCY"
	StateMedicationAskSymptoms     = "MEDICATION_ASK_SYMPTOMS"
	StateMedicationReceiveHistoria = "MEDICATION_RECEIVE_HISTORIA"
	StateMedicationReceiveOrden    = "MEDICATION_RECEIVE_ORDEN"
	StateMedicationConfirmOrden    = "MEDICATION_CONFIRM_ORDEN"
	StateMedicationPrepareSchedule = "MEDICATION_PREPARE_SCHEDULE"
)

// === Entity Management ===
const (
	StateCheckEntity     = "CHECK_ENTITY"
	StateConfirmEntity   = "CONFIRM_ENTITY"
	StateChangeEntity    = "CHANGE_ENTITY"
	StateAskClientType   = "ASK_CLIENT_TYPE"
	StateShowEntityList  = "SHOW_ENTITY_LIST"
	StateAskEntityNumber = "ASK_ENTITY_NUMBER"
	StateOutOfHoursMenu  = "OUT_OF_HOURS_MENU"
	// EPS régimen + Sanitas municipality steps (SIESA contract resolution)
	StateAskEpsRegimen       = "ASK_EPS_REGIMEN"
	StateConfirmMunicipality = "CONFIRM_MUNICIPALITY"
)

// === Registro de Paciente Nuevo ===
const (
	StateRegistrationStart   = "REGISTRATION_START"
	StateRegDocumentType     = "REG_DOCUMENT_TYPE"
	StateRegFirstSurname     = "REG_FIRST_SURNAME"
	StateRegSecondSurname    = "REG_SECOND_SURNAME"
	StateRegFirstName        = "REG_FIRST_NAME"
	StateRegSecondName       = "REG_SECOND_NAME"
	StateRegBirthDate        = "REG_BIRTH_DATE"
	StateRegGender           = "REG_GENDER"
	StateRegBloodType        = "REG_BLOOD_TYPE"
	StateRegMaritalStatus    = "REG_MARITAL_STATUS"
	StateRegEducationLevel   = "REG_EDUCATION_LEVEL"
	StateRegOccupation       = "REG_OCCUPATION"
	StateRegAddress          = "REG_ADDRESS"
	StateRegPhone            = "REG_PHONE"
	StateRegPhone2           = "REG_PHONE2"
	StateRegEmail            = "REG_EMAIL"
	StateRegMunicipality     = "REG_MUNICIPALITY"
	StateRegBarrio           = "REG_BARRIO"
	StateRegClientType       = "REG_CLIENT_TYPE"
	StateRegUserType         = "REG_USER_TYPE"
	StateRegAffiliationType  = "REG_AFFILIATION_TYPE"
	StateRegEntity           = "REG_ENTITY"
	StateRegZone             = "REG_ZONE"
	StateRegSelectCorrection = "REG_SELECT_CORRECTION"
	StateConfirmRegistration = "CONFIRM_REGISTRATION"
	StateCreatePatient       = "CREATE_PATIENT"
)

// === Consulta de Citas ===
const (
	StateFetchAppointments = "FETCH_APPOINTMENTS"
	StateListAppointments  = "LIST_APPOINTMENTS"
	StateAppointmentAction = "APPOINTMENT_ACTION"
	StateNoAppointments    = "NO_APPOINTMENTS"
)

// === Confirmación / Cancelación de Citas ===
const (
	StateConfirmAppointment   = "CONFIRM_APPOINTMENT"
	StateAppointmentConfirmed = "APPOINTMENT_CONFIRMED"
	StateCancelAppointment    = "CANCEL_APPOINTMENT"
	StateCancelReason         = "CANCEL_REASON"
	StateAppointmentCancelled = "APPOINTMENT_CANCELLED"
)

// === Confirmaciones desde notificaciones proactivas ===
const (
	StateConfirmRescheduleNotif  = "CONFIRM_RESCHEDULE_NOTIF"
	StateConfirmCancelNotif      = "CONFIRM_CANCEL_NOTIF"
	StateNotifPending            = "NOTIF_PENDING"             // Notificación proactiva escalada al agente (texto libre, sin respuesta o fallo en SIESA)
	StateNotifRescheduleFallback = "NOTIF_RESCHEDULE_FALLBACK" // Reprogramar sin slots → ofrece Confirmar/Cancelar
)

// === Orden Médica y OCR ===
const (
	StateSelectWaitingList  = "SELECT_WAITING_LIST" // elegir una cita ya en lista de espera o seguir como nueva
	StateAskMedicalOrder    = "ASK_MEDICAL_ORDER"
	StateUploadMedicalOrder = "UPLOAD_MEDICAL_ORDER"
	StateValidateOCR        = "VALIDATE_OCR"
	StateConfirmOCRResult   = "CONFIRM_OCR_RESULT"
	StateOCRFailed          = "OCR_FAILED"
	StateAskManualCups      = "ASK_MANUAL_CUPS"
	StateManualProcedure    = "MANUAL_PROCEDURE_INPUT"
	StateSelectProcedure    = "SELECT_PROCEDURE"

	// Consolidación EMG/NC de Fisiatría entre órdenes separadas (docs/DISENO-CONSOLIDACION-EMG-CITAS.md)
	StateCheckEmgConsolidation = "CHECK_EMG_CONSOLIDATION"
	StateConfirmConsolidate    = "CONFIRM_CONSOLIDATE"
	StateAskEmgOrder           = "ASK_EMG_ORDER"
	StateUploadEmgOrder        = "UPLOAD_EMG_ORDER"
)

// === Validaciones Médicas ===
const (
	StateCheckSpecialCups    = "CHECK_SPECIAL_CUPS"
	StateAskGestationalWeeks = "ASK_GESTATIONAL_WEEKS"
	StateCheckExisting       = "CHECK_EXISTING"
	StateAppointmentExists   = "APPOINTMENT_EXISTS"
	StateAskContrasted       = "ASK_CONTRASTED"
	StateAskPregnancy        = "ASK_PREGNANCY"
	StatePregnancyBlock      = "PREGNANCY_BLOCK"
	StateAskBabyWeight       = "ASK_BABY_WEIGHT"
	StateGfrCreatinine       = "GFR_CREATININE"
	StateGfrExamDate         = "GFR_EXAM_DATE"
	StateGfrHeight           = "GFR_HEIGHT"
	StateGfrWeight           = "GFR_WEIGHT"
	StateGfrDisease          = "GFR_DISEASE"
	StateGfrResult           = "GFR_RESULT"
	StateGfrNotEligible      = "GFR_NOT_ELIGIBLE"
	StateAskSedation         = "ASK_SEDATION"
	StateCheckPriorConsult   = "CHECK_PRIOR_CONSULTATION"
	StateCheckMRCLimit       = "CHECK_MRC_LIMIT"
	StateCheckAgeRestriction = "CHECK_AGE_RESTRICTION"
)

// === Búsqueda y Agendamiento ===
const (
	StateSearchSlots        = "SEARCH_SLOTS"
	StateCoverageNoConvenio = "COVERAGE_NO_CONVENIO"
	StateSlotSearchRetry    = "SLOT_SEARCH_RETRY"
	StateShowSlots          = "SHOW_SLOTS"
	StateNoSlotsAvailable   = "NO_SLOTS_AVAILABLE"
	StateOfferWaitingList   = "OFFER_WAITING_LIST"
	StateConfirmBooking     = "CONFIRM_BOOKING"
	StateReconfirmBooking   = "RECONFIRM_BOOKING"
	StateCreateAppointment  = "CREATE_APPOINTMENT"
	StateBookingSuccess     = "BOOKING_SUCCESS"
	StateBookingFailed      = "BOOKING_FAILED"
	// StateAdvanceNextProcedure es la vuelta del agente de Néctar que dio por gestionado el
	// procedimiento actual: el bot sigue con el siguiente grupo de la orden, o cierra si no queda.
	StateAdvanceNextProcedure = "ADVANCE_NEXT_PROCEDURE"
)

// === Post-Acción y Cierre ===
const (
	StatePostActionMenu  = "POST_ACTION_MENU"
	StateFallbackMenu    = "FALLBACK_MENU"
	StateFarewell        = "FAREWELL"
	StateTerminated      = "TERMINATED"
	StateOutOfHours      = "OUT_OF_HOURS"
	StateEscalateToAgent = "ESCALATE_TO_AGENT"
	StateEscalated       = "ESCALATED"
)

// StateType indica si un estado es automático o interactivo
type StateType int

const (
	StateTypeAutomatic   StateType = iota // Se ejecuta sin esperar input
	StateTypeInteractive                  // Espera input del usuario
)

// stateTypes define el tipo de cada estado
var stateTypes = map[string]StateType{
	// Saludo e Identificación
	StateCheckBusinessHours:        StateTypeAutomatic,
	StateGreeting:                  StateTypeAutomatic,
	StateMainMenu:                  StateTypeInteractive,
	StateAskDocumentType:           StateTypeInteractive,
	StateAskDocument:               StateTypeInteractive,
	StatePatientLookup:             StateTypeAutomatic,
	StateConfirmIdentity:           StateTypeInteractive,
	StateShowContactInfo:           StateTypeAutomatic,
	StateConfirmContactInfo:        StateTypeInteractive,
	StateAskUpdatePhone:            StateTypeInteractive,
	StateAskUpdateEmail:            StateTypeInteractive,
	StateUpdateContactInfo:         StateTypeAutomatic,
	StateShowResults:               StateTypeAutomatic,
	StateShowLocations:             StateTypeAutomatic,
	StateShowHelp:                  StateTypeAutomatic,
	StateMedicationCheckSanitas:    StateTypeAutomatic,
	StateMedicationCheckExcel:      StateTypeAutomatic,
	StateMedicationAskLastDate:     StateTypeInteractive,
	StateMedicationAskDose:         StateTypeInteractive,
	StateMedicationAskFrequency:    StateTypeInteractive,
	StateMedicationAskSymptoms:     StateTypeInteractive,
	StateMedicationReceiveHistoria: StateTypeInteractive,
	StateMedicationReceiveOrden:    StateTypeInteractive,
	StateMedicationConfirmOrden:    StateTypeInteractive,
	StateMedicationPrepareSchedule: StateTypeAutomatic,

	// Entity Management
	StateCheckEntity:         StateTypeAutomatic,
	StateConfirmEntity:       StateTypeInteractive,
	StateChangeEntity:        StateTypeInteractive,
	StateAskClientType:       StateTypeInteractive,
	StateShowEntityList:      StateTypeAutomatic,
	StateAskEntityNumber:     StateTypeInteractive,
	StateAskEpsRegimen:       StateTypeInteractive,
	StateConfirmMunicipality: StateTypeInteractive,
	StateOutOfHoursMenu:      StateTypeInteractive,

	// Registro
	StateRegistrationStart:   StateTypeInteractive,
	StateRegDocumentType:     StateTypeInteractive,
	StateRegFirstSurname:     StateTypeInteractive,
	StateRegSecondSurname:    StateTypeInteractive,
	StateRegFirstName:        StateTypeInteractive,
	StateRegSecondName:       StateTypeInteractive,
	StateRegBirthDate:        StateTypeInteractive,
	StateRegGender:           StateTypeInteractive,
	StateRegBloodType:        StateTypeInteractive,
	StateRegMaritalStatus:    StateTypeInteractive,
	StateRegAddress:          StateTypeInteractive,
	StateRegPhone:            StateTypeInteractive,
	StateRegPhone2:           StateTypeInteractive,
	StateRegEmail:            StateTypeInteractive,
	StateRegMunicipality:     StateTypeInteractive,
	StateRegBarrio:           StateTypeInteractive,
	StateRegClientType:       StateTypeInteractive,
	StateRegUserType:         StateTypeInteractive,
	StateRegAffiliationType:  StateTypeInteractive,
	StateRegEntity:           StateTypeInteractive,
	StateRegZone:             StateTypeInteractive,
	StateRegSelectCorrection: StateTypeInteractive,
	StateConfirmRegistration: StateTypeInteractive,
	StateCreatePatient:       StateTypeAutomatic,

	// Consulta de Citas
	StateFetchAppointments: StateTypeAutomatic,
	StateListAppointments:  StateTypeInteractive,
	StateAppointmentAction: StateTypeInteractive,
	StateNoAppointments:    StateTypeInteractive,

	// Confirmación / Cancelación
	StateConfirmAppointment:      StateTypeInteractive,
	StateAppointmentConfirmed:    StateTypeAutomatic,
	StateCancelAppointment:       StateTypeInteractive,
	StateCancelReason:            StateTypeInteractive,
	StateAppointmentCancelled:    StateTypeAutomatic,
	StateConfirmRescheduleNotif:  StateTypeInteractive,
	StateConfirmCancelNotif:      StateTypeInteractive,
	StateNotifPending:            StateTypeInteractive,
	StateNotifRescheduleFallback: StateTypeInteractive,

	// Orden Médica y OCR
	StateSelectWaitingList:  StateTypeInteractive,
	StateAskMedicalOrder:    StateTypeAutomatic,
	StateUploadMedicalOrder: StateTypeInteractive,
	StateValidateOCR:        StateTypeAutomatic,
	StateConfirmOCRResult:   StateTypeInteractive,
	StateOCRFailed:          StateTypeAutomatic,
	StateAskManualCups:      StateTypeInteractive,
	StateManualProcedure:    StateTypeInteractive,
	StateSelectProcedure:    StateTypeInteractive,

	StateCheckEmgConsolidation: StateTypeAutomatic,
	StateConfirmConsolidate:    StateTypeInteractive,
	StateAskEmgOrder:           StateTypeInteractive,
	StateUploadEmgOrder:        StateTypeInteractive,

	// Validaciones Médicas
	StateCheckSpecialCups:    StateTypeAutomatic,
	StateAskGestationalWeeks: StateTypeAutomatic, // Auto-shows prompt first time; stops chain by returning self
	StateCheckExisting:       StateTypeAutomatic,
	StateAppointmentExists:   StateTypeAutomatic,
	StateAskContrasted:       StateTypeAutomatic, // Auto-skips if not contrastable; prompts if contrastable
	StateAskPregnancy:        StateTypeAutomatic, // Auto-skips for males and babies; prompts for females >= 1
	StatePregnancyBlock:      StateTypeAutomatic,
	StateAskBabyWeight:       StateTypeInteractive,
	StateGfrCreatinine:       StateTypeInteractive,
	StateGfrExamDate:         StateTypeInteractive,
	StateGfrHeight:           StateTypeInteractive,
	StateGfrWeight:           StateTypeInteractive,
	StateGfrDisease:          StateTypeInteractive,
	StateGfrResult:           StateTypeAutomatic,
	StateGfrNotEligible:      StateTypeAutomatic,
	StateAskSedation:         StateTypeAutomatic, // Auto-skips if not sedatable; prompts if sedatable
	StateCheckPriorConsult:   StateTypeAutomatic,
	StateCheckMRCLimit:       StateTypeAutomatic,
	StateCheckAgeRestriction: StateTypeAutomatic,

	// Búsqueda y Agendamiento
	StateSearchSlots:          StateTypeAutomatic,
	StateCoverageNoConvenio:   StateTypeInteractive,
	StateSlotSearchRetry:      StateTypeInteractive,
	StateShowSlots:            StateTypeInteractive,
	StateNoSlotsAvailable:     StateTypeAutomatic,
	StateOfferWaitingList:     StateTypeInteractive,
	StateConfirmBooking:       StateTypeInteractive,
	StateReconfirmBooking:     StateTypeInteractive,
	StateCreateAppointment:    StateTypeAutomatic,
	StateBookingSuccess:       StateTypeAutomatic,
	StateBookingFailed:        StateTypeAutomatic,
	StateAdvanceNextProcedure: StateTypeAutomatic,

	// Post-Acción y Cierre
	StatePostActionMenu:  StateTypeInteractive,
	StateFallbackMenu:    StateTypeInteractive,
	StateFarewell:        StateTypeAutomatic,
	StateTerminated:      StateTypeAutomatic,
	StateOutOfHours:      StateTypeAutomatic,
	StateEscalateToAgent: StateTypeAutomatic,
	StateEscalated:       StateTypeAutomatic,
}

// IsAutomatic retorna true si el estado es automático.
// Unknown states default to Interactive (safe: waits for user input).
func IsAutomatic(state string) bool {
	st, ok := stateTypes[state]
	if !ok {
		return false // Unknown states are treated as Interactive
	}
	return st == StateTypeAutomatic
}

// BotDisabledStep es el paso con que se escala cuando el bot está apagado (BOT_ENABLED=false). NO
// es un estado de la máquina —no tiene handler— y por eso no empieza por `State`: solo nombra el
// paso del que sale el menú de retornos y lo que guarda `pre_escalation_state`.
const BotDisabledStep = "BOT_DISABLED"

// Claves del contexto de sesión que compartirán la máquina, el handler de escalación y el worker.
// Viven aquí, y no como literales en cada paquete, para que quien escriba y quien lea no se
// separen.
const (
	// CtxPreAutoState es el paso en que se quedó el paciente ANTES de encadenar hacia un estado
	// automático (la escalación, sobre todo). Sirve para decir dónde se atascó la conversación;
	// sin él el paso sería «ESCALATE_TO_AGENT», que no le sirve a nadie. Hoy lo escribe la
	// máquina al encadenar y lo lee el handler de escalación.
	CtxPreAutoState = "_pre_auto_state"

	// CtxPatientAskedForAgent ("1") marca que EL PACIENTE PIDIÓ la persona (palabra clave o
	// «agente» en el menú post-acción), frente a las escalaciones donde el bot se atasca. La
	// escriben el interceptor de palabras clave y el menú post-acción; la lee el handler de
	// escalación para decidir el `reason` del sobre y, en Néctar, si se le avisa al paciente, y la
	// borra al escalar. Es una marca aparte y no un valor de `escalation_reason` para no mezclar
	// motivos de diagnóstico con lo que ve el paciente.
	CtxPatientAskedForAgent = "patient_asked_for_agent"

	// CtxEscalationNextDay ("1") marca que la escalación en curso de esta sesión quedó encolada
	// para que una persona la atienda al abrir el siguiente día hábil (solo Néctar). La escribe el
	// handler de escalación cuando escala dentro de esa ventana y la borra en cualquier otra
	// escalación: describe SIEMPRE la escalación en curso. Se persiste (WithContext) porque la leen
	// turnos posteriores: el tope por sesión del handler, el checker de sesiones escaladas
	// (session.EscalationQueuedForNextDayKey) y el retorno del agente (worker), que la borra.
	CtxEscalationNextDay = "escalacion_proximo_dia"

	// CtxEscalationLastInput es lo último que el paciente escribió antes de que se agotaran los
	// reintentos (ya enmascarado por maskInputForLog y recortado a 120 runas). Lo escriben los
	// helpers de validación al agotar, lo lee el resumen del agente (grupo `intento`) y
	// escalationExit lo borra: describe SIEMPRE la escalación en curso.
	CtxEscalationLastInput = "escalation_last_input"

	// CtxOCRFailureCause es por qué falló la lectura de la orden (el error del OCR, o `ocr_error` /
	// `ocr_timeout`). Lo escribe processOrderMedia al fallar, lo borra al leer con éxito y lo lee el
	// resumen del agente (grupo `orden`).
	CtxOCRFailureCause = "ocr_failure_cause"

	// OCRFailureNoProcedures es la causa que se guarda cuando la lectura falló sin que el OCR diera
	// un motivo (`ocrResult.Error` vacío).
	OCRFailureNoProcedures = "ocr_sin_procedimientos"

	// CtxOCRUnreadNames son los nombres de los procedimientos que el OCR leyó SIN código, unidos por
	// «; ». Es lo único que el agente tiene de ellos. Lo escribe processOrderMedia y lo lee el
	// resumen del agente (grupo `orden`).
	CtxOCRUnreadNames = "ocr_unread_names"

	// CtxEscalationWaitingListID es el id de la entrada de lista de espera que el paciente tenía al
	// decir que no tenía la orden. Lo escribe has_order_no, lo lee el resumen del agente (grupo
	// `orden`) y escalationExit lo borra.
	CtxEscalationWaitingListID = "escalation_waiting_list_id"

	// CtxContactIdentifier es el contacto de la conversación de Néctar tal como lo da Néctar (E.164
	// sin «+», o el BSUID). Lo escribe el worker al llegar el mensaje (SessionManager.AttachContact,
	// junto con sessions.contact_identifier) y lo lee session.ContactPhoneOf. Es el mismo literal de
	// session.CtxContactIdentifier: aquí se reexporta para que el flujo no lo escriba a mano.
	CtxContactIdentifier = session.CtxContactIdentifier
)
