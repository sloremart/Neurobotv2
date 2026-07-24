package bird

import (
	"errors"
	"testing"
	"unicode/utf16"
)

// El truncado debe respetar el límite de WhatsApp medido en UNIDADES UTF-16 (como la Graph API):
// un emoji fuera del BMP cuenta 2, y nunca se parte un par sustituto.
func TestTruncateWhatsApp(t *testing.T) {
	cases := []struct {
		in  string
		max int
	}{
		{"Corto", 24},
		{"Aplicación de medicamentos", 24}, // 26 code units → recorta
		{"💉 Aplicación de medicamentos", 24},
		{"☢️ Agendar cita PET-CT", 24},
		{"AAAAAAAAAAAAAAAAAAAAAAAA💉", 24}, // 24 letras + emoji (2) = 26 → recorta, sin partir el emoji
	}
	for _, c := range cases {
		got := truncateWhatsApp(c.in, c.max)
		if n := len(utf16.Encode([]rune(got))); n > c.max {
			t.Errorf("truncateWhatsApp(%q) = %q tiene %d unidades UTF-16 (> %d)", c.in, got, n, c.max)
		}
		// No debe quedar un high surrogate suelto al final (emoji partido).
		u := utf16.Encode([]rune(got))
		if len(u) > 0 && u[len(u)-1] >= 0xD800 && u[len(u)-1] <= 0xDBFF {
			t.Errorf("truncateWhatsApp(%q) = %q partió un emoji (high surrogate al final)", c.in, got)
		}
	}
	// Cadena que cabe se devuelve intacta.
	if got := truncateWhatsApp("Ubicación", 24); got != "Ubicación" {
		t.Errorf("no debía tocar 'Ubicación', got %q", got)
	}
}

// Los errores de CONTENIDO de WhatsApp (131009 / "too long" / "not valid") se distinguen de los
// transitorios para elevarlos a ERROR (alerta) — la causa por la que el bloqueante no se auditaba.
func TestWhatsAppContentError(t *testing.T) {
	content := []error{
		errors.New("conversations api: status 400, body: (#131009) Parameter value is not valid, details: Row title is too long. Max length is 24"),
		errors.New(`bird: {"code":131009,"message":"..."}`),
		errors.New("something is too long"),
		errors.New("Parameter value is not valid"),
	}
	for _, e := range content {
		if _, ok := whatsAppContentError(e); !ok {
			t.Errorf("debía clasificar como error de contenido: %v", e)
		}
	}
	transient := []error{
		nil,
		errors.New("conversation not active"),
		errors.New("conversations api: status 131026, body: message undeliverable"), // número sin WhatsApp: transitorio
		errors.New("dial tcp: timeout"),
	}
	for _, e := range transient {
		if _, ok := whatsAppContentError(e); ok {
			t.Errorf("NO debía clasificar como contenido: %v", e)
		}
	}
}
