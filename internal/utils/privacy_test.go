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

func TestMaskDocument_Disabled(t *testing.T) {
	SetMaskPhones(false)
	defer SetMaskPhones(true)
	if got := MaskDocument("1000000689"); got != "1000000689" {
		t.Errorf("con masking off, MaskDocument debe devolver el valor intacto, got %q", got)
	}
}
