package handlers

import "testing"

// La fecha de nacimiento debe aceptar el formato NATURAL colombiano (DD/MM/AAAA, DD-MM-AAAA) además del
// ISO (AAAA-MM-DD, AAAA/MM/DD), sin ambigüedad entre año-primero y día-primero (§5g).
func TestParseBirthDate(t *testing.T) {
	ok := []struct {
		in    string
		wantY int
		wantM int
		wantD int
	}{
		{"1992-04-17", 1992, 4, 17},
		{"1992/04/17", 1992, 4, 17},
		{"17/04/1992", 1992, 4, 17}, // natural colombiano
		{"17-04-1992", 1992, 4, 17},
		{"05/12/2000", 2000, 12, 5}, // DD/MM: día 05, mes 12 (no MM/DD)
	}
	for _, c := range ok {
		d, valid := parseBirthDate(c.in)
		if !valid {
			t.Errorf("parseBirthDate(%q) = inválida; esperaba válida", c.in)
			continue
		}
		if d.Year() != c.wantY || int(d.Month()) != c.wantM || d.Day() != c.wantD {
			t.Errorf("parseBirthDate(%q) = %04d-%02d-%02d; esperaba %04d-%02d-%02d",
				c.in, d.Year(), d.Month(), d.Day(), c.wantY, c.wantM, c.wantD)
		}
	}

	bad := []string{"", "hola", "32/13/2000", "abc/def/ghi", "1992"}
	for _, in := range bad {
		if _, valid := parseBirthDate(in); valid {
			t.Errorf("parseBirthDate(%q) = válida; esperaba inválida", in)
		}
	}
}
