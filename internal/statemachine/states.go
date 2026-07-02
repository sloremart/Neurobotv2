package statemachine

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
	StateNotifPending            = "NOTIF_PENDING"             // Escalación por texto libre durante confirmación proactiva
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
	StateCheckBusinessHours: StateTypeAutomatic,
	StateGreeting:           StateTypeAutomatic,
	StateMainMenu:           StateTypeInteractive,
	StateAskDocumentType:    StateTypeInteractive,
	StateAskDocument:        StateTypeInteractive,
	StatePatientLookup:      StateTypeAutomatic,
	StateConfirmIdentity:    StateTypeInteractive,
	StateShowContactInfo:    StateTypeAutomatic,
	StateConfirmContactInfo: StateTypeInteractive,
	StateAskUpdatePhone:     StateTypeInteractive,
	StateAskUpdateEmail:     StateTypeInteractive,
	StateUpdateContactInfo:  StateTypeAutomatic,
	StateShowResults:        StateTypeAutomatic,
	StateShowLocations:      StateTypeAutomatic,
	StateShowHelp:           StateTypeAutomatic,

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
	StateSearchSlots:        StateTypeAutomatic,
	StateCoverageNoConvenio: StateTypeInteractive,
	StateSlotSearchRetry:    StateTypeInteractive,
	StateShowSlots:          StateTypeInteractive,
	StateNoSlotsAvailable:   StateTypeAutomatic,
	StateOfferWaitingList:   StateTypeInteractive,
	StateConfirmBooking:     StateTypeInteractive,
	StateReconfirmBooking:   StateTypeInteractive,
	StateCreateAppointment:  StateTypeAutomatic,
	StateBookingSuccess:     StateTypeAutomatic,
	StateBookingFailed:      StateTypeAutomatic,

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
