package utils

import "testing"

func TestParseColombianPhone(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		// Single number formats
		{"3001234567", "+573001234567"},
		{"3101234567", "+573101234567"},
		{"+573001234567", "+573001234567"},
		{"573001234567", "+573001234567"},
		{"+57 300 123 4567", "+573001234567"},
		{"300-123-4567", "+573001234567"},
		{"300/123/4567", "+573001234567"},
		{"300.123.4567", "+573001234567"},

		// Two numbers separated by various separators — take first
		{"3107558761 3125920492", "+573107558761"},
		{"3107558761-3125920492", "+573107558761"},
		{"3107558761,3125920492", "+573107558761"},
		{"3107558761.3125920492", "+573107558761"},
		{"3107558761/3125920492", "+573107558761"},

		// With 57 prefix, no +
		{"573107558761", "+573107558761"},

		// Invalid formats
		{"", ""},
		{"null", ""},
		{"no tiene", ""},
		{"n/a", ""},
		{"-", ""},
		{"1234567", ""},    // Too short
		{"6011234567", ""}, // Not mobile (doesn't start with 3)
		{"1234567890", ""}, // 10 digits but doesn't start with 3

		// Malformados: longitud incorrecta → "" (NO rescatar a un número equivocado).
		{"33001234567", ""},               // 11 dígitos (un dígito de más) — antes devolvía un número erróneo
		{"30012345678", ""},               // 11 dígitos
		{"300123456", ""},                 // 9 dígitos
		{"5730012345678", ""},             // 13 dígitos
		{"300 1234 567", "+573001234567"}, // espaciado raro pero 10 dígitos válidos
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := ParseColombianPhone(tc.input)
			if got != tc.expected {
				t.Errorf("ParseColombianPhone(%q) = %q, want %q", tc.input, got, tc.expected)
			}
		})
	}
}

func TestSamePhone(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"+573001234567", "573001234567", true},          // con vs sin '+'
		{"+573001234567", "3001234567", true},            // E.164 vs nacional (últimos 10)
		{"573001234567", "3001234567", true},             // con país vs sin país
		{"+57 300 123 4567", "+573001234567", true},      // espacios
		{"whatsapp:+573001234567", "573001234567", true}, // prefijo de canal
		{"+573001234567", "+573009999999", false},        // distintos
		{"+573001234567", "", false},                     // vacío
		{"", "", false},                                  // ambos vacíos
		{"123", "456", false},                            // cortos y distintos
	}
	for _, c := range cases {
		if got := SamePhone(c.a, c.b); got != c.want {
			t.Errorf("SamePhone(%q,%q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}
