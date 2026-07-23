package handlers

import "testing"

// Casos REALES de invalid_input en ASK_DOCUMENT_TYPE (par.8.1 #7): variantes obvias resuelven; el
// conflicto numero-vs-texto NO adivina; numero largo de cedula queda para looksLikeDocNumber.
func TestParseDocType_Variants(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"1", "CC"},
		{"CC", "CC"},
		{"cc", "CC"}, // comportamiento previo intacto
		{"1 cc", "CC"},
		{"1.", "CC"},
		{"1cc", "CC"},
		{"1.cc", "CC"},
		{"1 cédula de ciudadanía", "CC"},
		{"cédula de ciudadanía", "CC"},
		{"cedula", "CC"},
		{"1 tarjeta de identidad", ""}, // conflicto: no adivinar
		{"121947913", ""},              // numero de cedula: lo maneja looksLikeDocNumber
		{"hola", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := parseDocType(c.in); got != c.want {
			t.Errorf("parseDocType(%q) = %q, esperaba %q", c.in, got, c.want)
		}
	}
}

// Saludos e intenciones a mitad de flujo (par.8.1 #10).
func TestIsGreetingOrIntent(t *testing.T) {
	for _, in := range []string{"hola", "Hola!", "buenas tardes", "agendar", "quiero una cita", "gracias"} {
		if !isGreetingOrIntent(in) {
			t.Errorf("%q debía reconocerse como saludo/intención", in)
		}
	}
	for _, in := range []string{"sanitas", "3", "capital salud", "xyz"} {
		if isGreetingOrIntent(in) {
			t.Errorf("%q NO debía reconocerse como saludo/intención", in)
		}
	}
}
