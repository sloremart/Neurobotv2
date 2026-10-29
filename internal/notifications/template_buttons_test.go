package notifications

import "testing"

func TestTemplateButtonPayload(t *testing.T) {
	cases := []struct {
		id     string
		want   string
		wantOK bool
	}{
		{"Confirmar", "confirm", true},
		{"Reprogramar", "reprogramar", true},
		{"Cancelar", "cancelar", true},
		{"Entendido", "understood", true},
		{"Agendar", "wl_schedule", true},
		{"No, gracias", "wl_decline", true},
		{"no gracias", "wl_decline", true},
		{"NO GRACIAS", "wl_decline", true},
		{"  confírmar ", "confirm", true},
		// Un payload ya es payload: se devuelve igual.
		{"confirm", "confirm", true},
		{"reschedule", "reschedule", true},
		{"wl_decline", "wl_decline", true},
		{"hola", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		got, ok := TemplateButtonPayload(c.id)
		if got != c.want || ok != c.wantOK {
			t.Errorf("TemplateButtonPayload(%q) = (%q, %v), quiero (%q, %v)", c.id, got, ok, c.want, c.wantOK)
		}
	}
}

// Cada botón que HandleInvalidInput reenvía se traduce de vuelta a SU payload, salvo el
// «Reprogramar» de la cancelación (payload reschedule), cuyo título comparte con el de la
// confirmación: los dos acaban en "reschedule" en normalizePostback, así que da igual cuál salga.
func TestTemplateButtonPayload_RoundTripsEveryResentButton(t *testing.T) {
	for typ, buttons := range resentButtons {
		for _, b := range buttons {
			got, ok := TemplateButtonPayload(b.Title)
			if !ok {
				t.Fatalf("%s: el título %q no se traduce", typ, b.Title)
			}
			if normalizePostback(got) != normalizePostback(b.Payload) {
				t.Errorf("%s: %q → %q, que no equivale a %q", typ, b.Title, got, b.Payload)
			}
		}
	}
}
