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
)

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
)
