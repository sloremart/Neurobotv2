package utils

import (
	"testing"
	"unicode/utf8"
)

func TestMaskDocument(t *testing.T) {
	// Asegurar masking activo (estado global por defecto).
	SetMaskPhones(true)
	cases := []struct {
		name, in, want string
	}{
		{"corto se oculta del todo", "1234", "***"},
		{"cedula tipica", "1000000689", "10***89"},
		{"vacio", "", "***"},
		{"con acentos al borde", "Iván", "***"},        // 4 runas → oculto total
		{"largo con acentos", "José Núñez", "Jo***ez"}, // recorte por runas, no bytes
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := MaskDocument(c.in)
			if got != c.want {
				t.Errorf("MaskDocument(%q) = %q, want %q", c.in, got, c.want)
			}
			if !utf8.ValidString(got) {
				t.Errorf("MaskDocument(%q) produjo UTF-8 inválido: %q", c.in, got)
			}
		})
	}
}

// El texto libre del paciente (cédula, nombres, dirección, síntomas) no llega a los logs: solo su
// largo, para que la guía de auditoría siga viendo que el mensaje entró al motor.
func TestRedactLogAttr_PatientText(t *testing.T) {
	SetMaskPhones(true)
	cases := []struct{ key, in, want string }{
		{"text", "1000000689 Juan Pérez", "[redactado: 21 caracteres]"},
		{"Text", "hola", "[redactado: 4 caracteres]"},
		{"text", "", ""},
		{"state", "ASK_DOCUMENT", "ASK_DOCUMENT"},
	}
	for _, c := range cases {
		if got := RedactLogAttr(c.key, c.in); got != c.want {
			t.Errorf("RedactLogAttr(%q, %q) = %q, want %q", c.key, c.in, got, c.want)
		}
	}
}

// C-22: LOG_MASK_PHONES=false solo destapa los teléfonos; documento, nombre y texto libre del
// paciente se siguen redactando aunque alguien apague el interruptor para depurar un número.
func TestMaskPhonesOff_OnlyAffectsPhones(t *testing.T) {
	SetMaskPhones(false)
	defer SetMaskPhones(true)
	if got := MaskPhone("+573103343616"); got != "+573103343616" {
		t.Errorf("con LOG_MASK_PHONES=false el teléfono sale entero, got %q", got)
	}
	if got := MaskDocument("1000000689"); got != "10***89" {
		t.Errorf("MaskDocument debe enmascarar aunque el interruptor de teléfonos esté apagado, got %q", got)
	}
	if got := MaskText("1000000689 Juan Pérez"); got != "[redactado: 21 caracteres]" {
		t.Errorf("MaskText debe redactar aunque el interruptor de teléfonos esté apagado, got %q", got)
	}
	if got := RedactLogAttr("patient_name", "José Núñez"); got != "Jo***ez" {
		t.Errorf("el nombre se redacta aunque el interruptor de teléfonos esté apagado, got %q", got)
	}
}

// C-21: las claves de texto libre y de contacto que llegan a los logs (también en Debug) se
// redactan; las que comparten nombre con datos del flujo (p. ej. "to" de una transición de
// estado) no se tocan.
func TestRedactLogAttr_FreeTextAndContactKeys(t *testing.T) {
	SetMaskPhones(true)
	cases := []struct{ key, in, want string }{
		{"input", "1000000689", "[redactado: 10 caracteres]"},
		{"last_input", "mi cédula es 1000000689", "[redactado: 23 caracteres]"},
		{"email", "maria@example.com", "[redactado: 17 caracteres]"},
		{"correo", "maria@example.com", "[redactado: 17 caracteres]"},
		{"direccion", "Cra 1 # 2-3", "[redactado: 11 caracteres]"},
		{"observaciones", "dolor lumbar", "[redactado: 12 caracteres]"},
		{"numero_documento", "1000000689", "10***89"},
		{"patient_phone", "+573103343616", "+573***3616"},
		{"to", "ASK_DOCUMENT", "ASK_DOCUMENT"},
		{"payload", "appt_confirm", "appt_confirm"},
	}
	for _, c := range cases {
		if got := RedactLogAttr(c.key, c.in); got != c.want {
			t.Errorf("RedactLogAttr(%q, %q) = %q, want %q", c.key, c.in, got, c.want)
		}
	}
}

// H2: los datos de un evento son un mapa y el ReplaceAttr global no recorre sus claves. RedactLogMap
// devuelve una copia redactada clave por clave (también anidada) y no toca el original, que es el
// que se guarda en la BD.
func TestRedactLogMap(t *testing.T) {
	SetMaskPhones(true)
	original := map[string]interface{}{
		"phone":  "+573103343616",
		"email":  "maria@example.com",
		"doc":    "1000000689",
		"count":  2,
		"state":  "ASK_DOCUMENT",
		"nested": map[string]interface{}{"patient_name": "José Núñez", "cups": "890274"},
	}
	got := RedactLogMap(original)
	want := map[string]interface{}{
		"phone": "+573***3616", "email": "[redactado: 17 caracteres]", "doc": "10***89",
		"count": 2, "state": "ASK_DOCUMENT",
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("RedactLogMap[%q] = %v, want %v", k, got[k], w)
		}
	}
	nested, _ := got["nested"].(map[string]interface{})
	if nested["patient_name"] != "Jo***ez" || nested["cups"] != "890274" {
		t.Errorf("el mapa anidado se redacta clave por clave: %v", nested)
	}
	if original["phone"] != "+573103343616" || original["nested"].(map[string]interface{})["patient_name"] != "José Núñez" {
		t.Errorf("el mapa original no se modifica: %v", original)
	}
	// El nombre del paciente también viaja con la clave "patient" (llamada IVR de prueba).
	if got := RedactLogAttr("patient", "José Núñez"); got != "Jo***ez" {
		t.Errorf("RedactLogAttr(patient) = %q", got)
	}
	if RedactLogMap(nil) != nil {
		t.Error("un mapa nil sigue nil")
	}
}

// Lo que se persiste (chat_events) no depende de LOG_MASK_PHONES: esa bandera solo destapa logs.
func TestMaskPhoneAlways_IgnoresLogFlag(t *testing.T) {
	SetMaskPhones(false)
	defer SetMaskPhones(true)
	// Como mucho los últimos 4 dígitos, sea móvil o fijo; menos si es corto.
	cases := []struct{ in, want string }{
		{"+573103343616", "***3616"},
		{"+57 310 334 3616", "***3616"},
		{"6014567890", "***7890"},
		{"3001234", "***34"},
		{"12345", "***45"},
		{"1234", "***"},
		{"", "***"},
		{"***3616", "***3616"}, // ya enmascarado queda igual (la retención repasa filas)
		{"***34", "***34"},
	}
	for _, c := range cases {
		if got := MaskPhoneAlways(c.in); got != c.want {
			t.Errorf("MaskPhoneAlways(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if got := MaskPhone("+573103343616"); got != "+573103343616" {
		t.Errorf("MaskPhone con la bandera apagada sigue destapando el log: %q", got)
	}
}

func TestMaskEmail(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"típico", "juan.perez@gmail.com", "ju***@gmail.com"},
		{"usuario corto", "ab@x.co", "***@x.co"},
		{"usuario con tilde", "josé.núñez@correo.co", "jo***@correo.co"},
		{"sin arroba", "juanperez", "***"},
		{"vacío", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := MaskEmail(c.in)
			if got != c.want {
				t.Errorf("MaskEmail(%q) = %q, want %q", c.in, got, c.want)
			}
			if !utf8.ValidString(got) {
				t.Errorf("MaskEmail(%q) produjo UTF-8 inválido: %q", c.in, got)
			}
		})
	}
}

// Solo se tapan las secuencias de 5 o más dígitos (documento, teléfono), con espacios, puntos o
// guiones dentro; quedan a la vista como mucho sus 2 últimos dígitos. El resto del texto queda igual.
func TestMaskDigitRuns(t *testing.T) {
	cases := []struct{ in, want string }{
		{"1000000689", "***89"},
		{"1.000.000.689", "***89"},
		{"100 000 0689", "***89"},
		{"no, mi cedula es 1000000689 gracias", "no, mi cedula es ***89 gracias"},
		{"mi cel 300 123 4567 y fijo 601-456-7890", "mi cel ***67 y fijo ***90"},
		{"cc 1000000689.", "cc ***89."},
		{"no soy yo", "no soy yo"},
		{"opción 2", "opción 2"},
		{"1234", "1234"},
		{"12-34", "12-34"},
		{"15/08/2026", "15/08/2026"},
		{"", ""},
	}
	for _, c := range cases {
		if got := MaskDigitRuns(c.in); got != c.want {
			t.Errorf("MaskDigitRuns(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// E1-M1: los umbrales exactos de MaskDigitRuns (5 dígitos tapan, 4 no) y de MaskPhoneAlways (de 5 a 9
// caracteres dejan 2, de 10 en adelante dejan 4, hasta 4 ninguno).
func TestMaskDigitRuns_Thresholds(t *testing.T) {
	cases := []struct{ in, want string }{
		{"12345", "***45"},
		{"123456", "***56"},
		{"1234", "1234"},
		{"tel 1234 y 98765", "tel 1234 y ***65"},
	}
	for _, c := range cases {
		if got := MaskDigitRuns(c.in); got != c.want {
			t.Errorf("MaskDigitRuns(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestMaskPhoneAlways_LengthBoundaries(t *testing.T) {
	cases := []struct{ in, want string }{
		{"12345678", "***78"},     // 8 caracteres: 2
		{"123456789", "***89"},    // 9 caracteres: 2
		{"1234567890", "***7890"}, // 10 caracteres: 4
		{"1234", "***"},           // 4: ninguno
	}
	for _, c := range cases {
		if got := MaskPhoneAlways(c.in); got != c.want {
			t.Errorf("MaskPhoneAlways(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
