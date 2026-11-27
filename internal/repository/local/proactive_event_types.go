package local

// proactiveEventTypes son los eventos del envío proactivo y del IVR que se registran con el teléfono
// de SIESA (+57…) y sin sesión: no hay de dónde copiar la línea de Néctar, y esos envíos salen
// siempre por la línea principal. Con el puente encendido se etiquetan 'main' (nectar_line); sin
// puente quedan NULL, como en Bird.
//
// Lista cerrada. Quedan FUERA los eventos que se registran con la clave del pending (que en Néctar
// es nectar:<id> y puede ser de la línea vieja) o con la sesión: notification_confirmed,
// notification_cancel_acknowledged, waiting_list_schedule_declined, notification_escalated_agent,
// notification_ivr_sent, notification_siesa_unattended y notification_sent.
var proactiveEventTypes = map[string]struct{}{
	"reminder_prep_sent":               {},
	"reminder_suppressed_no_whatsapp":  {},
	"notification_send_failed":         {},
	"notification_in_doubt":            {},
	"notification_skipped_agent_owned": {},
	"notification_confirmed_ivr":       {},
	"notification_cancelled_ivr":       {},
	"notification_timeout":             {},
}

// isProactiveEventType dice si el tipo de evento es de la lista cerrada de arriba.
func isProactiveEventType(t string) bool {
	_, ok := proactiveEventTypes[t]
	return ok
}
