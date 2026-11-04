package statemachine

// MOTIVOS INTERNOS DE ESCALACIÓN (`escalation_reason`).
//
// Son literales de cable: viajan como `reason_detail` del protocolo v1, que Néctar le muestra al
// agente y valida con la forma `^[a-z_]{1,64}$`. Viven aquí, y no en quien escala, porque también
// los lee quien arma el menú de retornos (handlers) para ofrecer las acciones de cada motivo.

// CtxEscalationReason es la clave de sesión con el motivo interno de una escalación del flujo.
const CtxEscalationReason = "escalation_reason"

// Motivos de las escalaciones del NotificationManager.
const (
	// EscalationReasonNotifNoResponse: el paciente no respondió la confirmación (ni WhatsApp ni IVR).
	EscalationReasonNotifNoResponse = "notificacion_sin_respuesta"
	// EscalationReasonNotifSystemFailure: el paciente confirmó, pero falló la persistencia en SIESA.
	EscalationReasonNotifSystemFailure = "notificacion_fallo_sistema"
	// EscalationReasonNotifFreeText: el paciente respondió la notificación con texto libre repetido.
	EscalationReasonNotifFreeText = "confirmacion_texto_libre"
	// EscalationReasonRescheduleNoProcedure: el paciente pidió reprogramar desde la notificación,
	// pero la cita no tiene procedimiento (CUPS) en SIESA; la reprograma un agente a mano.
	EscalationReasonRescheduleNoProcedure = "reprogramar_sin_procedimiento"
	// EscalationReasonNotifSiesaPending: el paciente respondió al recordatorio con SIESA caída y la
	// respuesta pendiente no se pudo aplicar sola (venció un tope, la cita cambió o el paciente
	// estaba en otra conversación con el bot). CtxNotifSiesaCase dice cuál y elige el menú.
	EscalationReasonNotifSiesaPending = "notificacion_siesa_pendiente"
)

// Motivos de las escalaciones del flujo del paciente. Cada sitio que manda a ESCALATE_TO_AGENT
// escribe el suyo con WithContext(CtxEscalationReason, …): como `reason` (enum) no se le muestra
// al agente de Néctar, este detalle es lo único que le dice POR QUÉ llegó el caso.
const (
	// EscalationReasonPatientAsked: el paciente pidió un asesor (palabra clave o «agente» del menú).
	EscalationReasonPatientAsked = "paciente_pidio_asesor"
	// EscalationReasonMaxRetries: se agotaron los reintentos de una respuesta inválida.
	EscalationReasonMaxRetries = "max_reintentos"
	// EscalationReasonAIRecoveryExhausted: la recuperación con IA agotó su presupuesto.
	EscalationReasonAIRecoveryExhausted = "ia_recuperacion_agotada"
	// EscalationReasonParticularNoOrder: paciente particular sin orden médica.
	EscalationReasonParticularNoOrder = "particular_sin_orden"
	// EscalationReasonOCRAskedForAgent: el paciente pidió el agente al subir la orden.
	EscalationReasonOCRAskedForAgent = "ocr_pidio_agente"
	// EscalationReasonOCRNoCode: la orden no tenía ningún código legible.
	EscalationReasonOCRNoCode = "ocr_sin_codigo"
	// EscalationReasonOCRInvalidJSON: el resultado del OCR guardado no se pudo leer.
	EscalationReasonOCRInvalidJSON = "ocr_json_invalido"
	// EscalationReasonSanitasBlock: bloqueo por reglas de SANITAS.
	EscalationReasonSanitasBlock = "bloqueo_sanitas"
	// EscalationReasonNoValidCups: ningún CUPS de la orden es válido.
	EscalationReasonNoValidCups = "cups_no_validos"
	// EscalationReasonEmptyGrouping: la agrupación de procedimientos quedó vacía.
	EscalationReasonEmptyGrouping = "agrupacion_vacia"
	// EscalationReasonNoCoverage: el contrato no cubre el procedimiento y el paciente pidió agente.
	EscalationReasonNoCoverage = "sin_convenio"
	// EscalationReasonSleepStudy: estudio de sueño, lo agenda una persona.
	EscalationReasonSleepStudy = "estudio_sueno"
	// EscalationReasonNoEntities: no hay entidades para mostrar.
	EscalationReasonNoEntities = "sin_entidades"
	// EscalationReasonEntityUnresolved: la entidad elegida no se pudo resolver.
	EscalationReasonEntityUnresolved = "entidad_no_resuelta"
	// EscalationReasonParticularLiga: entidad «particular liga» (50001), la atiende una persona.
	EscalationReasonParticularLiga = "particular_liga"
	// EscalationReasonMedicationError: falló la preparación de la agenda de medicamentos.
	EscalationReasonMedicationError = "medicamentos_error"
	// EscalationReasonEmgConsolidationFailed: no se pudo consolidar el bloque EMG/NC.
	EscalationReasonEmgConsolidationFailed = "consolidacion_emg_fallida"
	// EscalationReasonEmgAskedForAgent: el paciente pidió el agente al subir la orden EMG.
	EscalationReasonEmgAskedForAgent = "emg_pidio_agente"
	// EscalationReasonBotDisabled: el bot está desactivado (kill switch).
	EscalationReasonBotDisabled = "bot_desactivado"
	// EscalationReasonAgentInputRejected: el dato que devolvió el agente no sirvió.
	EscalationReasonAgentInputRejected = "dato_agente_rechazado"
	// EscalationReasonRescheduleNoSlots: no hubo horarios para reprogramar y el paciente pidió
	// reprogramar igual; lo hace un agente a mano (la cita original sigue vigente).
	EscalationReasonRescheduleNoSlots = "reprogramar_sin_horarios"
	// EscalationReasonQuantityOutOfRange: la orden trae un CUPS con una cantidad por encima del
	// tope que el bot agenda solo; la nota para el agente dice cuál y cuánta.
	EscalationReasonQuantityOutOfRange = "cantidad_fuera_de_rango"
	// EscalationReasonOCRFailedReads: se agotaron las lecturas fallidas seguidas de la orden en la
	// sesión (error, tiempo agotado o lectura sin procedimientos).
	EscalationReasonOCRFailedReads = "ocr_lecturas_fallidas"
	// EscalationReasonCupsPossibleMisread: un código de la orden no se pudo agendar tal cual y el
	// catálogo trae procedimientos de nombre parecido, ninguno a un dígito del leído; la nota para el
	// agente dice qué se leyó y cuáles son los candidatos.
	EscalationReasonCupsPossibleMisread = "cups_posible_mala_lectura"
)

// EscalationReasons lista TODOS los motivos de cable: las notificaciones y los del flujo.
func EscalationReasons() []string {
	return []string{
		EscalationReasonNotifNoResponse, EscalationReasonNotifSystemFailure, EscalationReasonNotifFreeText,
		EscalationReasonRescheduleNoProcedure, EscalationReasonNotifSiesaPending, EscalationReasonPatientAsked, EscalationReasonMaxRetries, EscalationReasonAIRecoveryExhausted,
		EscalationReasonParticularNoOrder, EscalationReasonOCRAskedForAgent, EscalationReasonOCRNoCode,
		EscalationReasonOCRInvalidJSON, EscalationReasonSanitasBlock, EscalationReasonNoValidCups,
		EscalationReasonEmptyGrouping, EscalationReasonNoCoverage, EscalationReasonSleepStudy,
		EscalationReasonNoEntities, EscalationReasonEntityUnresolved, EscalationReasonParticularLiga,
		EscalationReasonMedicationError, EscalationReasonEmgConsolidationFailed,
		EscalationReasonEmgAskedForAgent, EscalationReasonBotDisabled, EscalationReasonAgentInputRejected,
		EscalationReasonRescheduleNoSlots, EscalationReasonQuantityOutOfRange, EscalationReasonOCRFailedReads,
		EscalationReasonCupsPossibleMisread,
	}
}

// Claves de sesión de una escalación de notificación (paso NOTIF_PENDING).
const (
	// CtxNotifEscalationDetail guarda el motivo de la escalación de la notificación: el menú de
	// NOTIF_PENDING cambia con él (el fallo de sistema ofrece «ya la confirmé a mano»).
	CtxNotifEscalationDetail = "notif_escalation_detail"
	// CtxNotifWaitingListID es el id de la entrada de lista de espera notificada: sin él, la acción
	// «agendar» del agente no sabría qué oferta tomar.
	CtxNotifWaitingListID = "notif_wl_id"
	// CtxNotifCupsCode es el código CUPS del primer procedimiento de la cita notificada.
	CtxNotifCupsCode = "notif_cups_code"
	// CtxNotifSiesaCase dice, en una escalación EscalationReasonNotifSiesaPending, qué quedó sin
	// aplicar: uno de los NotifSiesaCase*.
	CtxNotifSiesaCase = "notif_siesa_case"
)

// Casos de una respuesta pendiente de SIESA que pasa a un agente (CtxNotifSiesaCase).
const (
	// NotifSiesaCaseConfirm: el paciente confirmó y la confirmación no llegó a SIESA a tiempo.
	NotifSiesaCaseConfirm = "confirm"
	// NotifSiesaCaseCancel: el paciente pidió cancelar y no se le pudo pedir la confirmación de la
	// cancelación (venció un tope o estaba en otra conversación con el bot).
	NotifSiesaCaseCancel = "cancel"
	// NotifSiesaCaseChanged: cuando SIESA volvió, la cita ya había cambiado; no se aplicó nada.
	NotifSiesaCaseChanged = "changed"
)
