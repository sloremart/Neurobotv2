package api

import "testing"

func TestNormalizeQueryPhone(t *testing.T) {
	cases := map[string]string{
		"":              "",
		"573001234567":  "+573001234567",
		"+573001234567": "+573001234567",
		"nectar:1":      "nectar:1",
		"nectar:987":    "nectar:987",
	}
	for in, want := range cases {
		if got := normalizeQueryPhone(in); got != want {
			t.Errorf("normalizeQueryPhone(%q) = %q, quiero %q", in, got, want)
		}
	}
}
