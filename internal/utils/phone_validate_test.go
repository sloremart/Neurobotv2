package utils

import "testing"

func TestIsE164(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"+573001234567", true},
		{"+31612345678", true},
		{"+12025550123", true},
		{"573001234567", false},    // sin +
		{"felipe.rubio", false},    // nombre (caso real del incidente Bird)
		{"maria gomez", false},     // nombre con espacio
		{"+57 300 1234567", false}, // E.164 no admite espacios
		{"+abc", false},
		{"", false},
		{"+", false},
		{"+1234567", false},          // muy corto (<8 dígitos)
		{"+1234567890123456", false}, // muy largo (>15 dígitos)
	}
	for _, tc := range cases {
		if got := IsE164(tc.in); got != tc.want {
			t.Errorf("IsE164(%q)=%v, want %v", tc.in, got, tc.want)
		}
	}
}
