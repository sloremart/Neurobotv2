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
