package notifications

import (
	"strings"

	"github.com/neuro-bot/neuro-bot/internal/bird"
)

// templateButton es un botón de respuesta rápida: el título que ve el paciente y el payload con
// el que lo despacha HandleResponse.
type templateButton struct {
	Title   string
	Payload string
}

// resentButtons es la ÚNICA tabla de botones por tipo de notificación: la lee HandleInvalidInput
// para reenviarlos y TemplateButtonPayload para traducir de vuelta el título que manda Néctar.
var resentButtons = map[string][]templateButton{
	"confirmation": {
		{Title: "Confirmar", Payload: "confirm"},
		{Title: "Reprogramar", Payload: "reprogramar"},
		{Title: "Cancelar", Payload: "cancelar"},
	},
	"cancellation": {
		{Title: "Entendido", Payload: "understood"},
		{Title: "Reprogramar", Payload: "reschedule"},
	},
	"waiting_list": {
		{Title: "Agendar", Payload: "wl_schedule"},
		{Title: "No, gracias", Payload: "wl_decline"},
	},
}

// buttonsFor devuelve los botones de un tipo de notificación en el formato de envío.
// "reschedule" reenvía los de la confirmación (mismo mensaje de gestión de la cita).
func buttonsFor(notifType string) []bird.Button {
	if notifType == "reschedule" {
		notifType = "confirmation"
	}
	src := resentButtons[notifType]
	out := make([]bird.Button, 0, len(src))
	for _, b := range src {
		out = append(out, bird.Button{Text: b.Title, Payload: b.Payload})
	}
	return out
}

// answersType dice si payload (ya traducido del título) es uno de los botones de la plantilla de
// notifType, comparados tras normalizePostback («cancelar» y «cancel» son el mismo botón).
func answersType(notifType, payload string) bool {
	if notifType == "reschedule" {
		notifType = "confirmation"
	}
	got := normalizePostback(payload)
	for _, b := range resentButtons[notifType] {
		if normalizePostback(b.Payload) == got {
			return true
		}
	}
	return false
}

// titleOrder fija el orden de búsqueda por título. «Reprogramar» aparece dos veces (reprogramar
// en la confirmación, reschedule en la cancelación); gana el primero, y da igual: normalizePostback
// lleva los dos a "reschedule" antes de despachar por tipo.
var titleOrder = []string{"confirmation", "cancellation", "waiting_list"}

// buttonKeyReplacer quita tildes para comparar títulos.
var buttonKeyReplacer = strings.NewReplacer("á", "a", "é", "e", "í", "i", "ó", "o", "ú", "u", "ü", "u", "ñ", "n")

// buttonKey reduce un título a minúsculas sin tildes y solo letras/dígitos: «No, gracias»,
// «no gracias» y «NO GRACIAS» dan la misma clave.
func buttonKey(s string) string {
	s = buttonKeyReplacer.Replace(strings.ToLower(s))
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// TemplateButtonPayload traduce el título de un botón de plantilla (lo que Néctar manda como
// interactive_reply_id en una respuesta rápida) al payload que entiende HandleResponse. Un id que
// ya es un payload de la tabla se devuelve igual. false = no es un botón de notificación.
func TemplateButtonPayload(id string) (string, bool) {
	for _, typ := range titleOrder {
		for _, b := range resentButtons[typ] {
			if id == b.Payload {
				return id, true
			}
		}
	}
	key := buttonKey(id)
	if key == "" {
		return "", false
	}
	for _, typ := range titleOrder {
		for _, b := range resentButtons[typ] {
			if key == buttonKey(b.Title) {
				return b.Payload, true
			}
		}
	}
	return "", false
}
