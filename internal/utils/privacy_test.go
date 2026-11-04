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
