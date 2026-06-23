package handlers

import "testing"

func TestResolveEPSContract(t *testing.T) {
	cases := []struct {
		name              string
		entity, regimen   string
		depCode, muniCode string
		want              string
	}{
		// SANITAS — MRC municipalities (dep 50)
		{"sanitas mrc acacias contributivo", entitySanitas, "1", "50", "006", "6"},
		{"sanitas mrc acacias subsidiado", entitySanitas, "2", "50", "006", "5"},
		{"sanitas mrc san martin contributivo", entitySanitas, "1", "50", "689", "6"},
		// SANITAS — Evento (Villavicencio 50-001 is NOT MRC)
		{"sanitas villavicencio contributivo evento", entitySanitas, "1", "50", "001", "4"},
		{"sanitas villavicencio subsidiado evento", entitySanitas, "2", "50", "001", "7"},
		// SANITAS — other department → Evento
		{"sanitas bogota contributivo evento", entitySanitas, "1", "11", "001", "4"},
		// MRC code but wrong department → Evento
		{"sanitas mrc-code wrong-dep evento", entitySanitas, "1", "25", "006", "4"},
		// Other EPS (municipality irrelevant)
		{"salud total contributivo", entitySaludTotal, "1", "", "", "13"},
		{"salud total subsidiado", entitySaludTotal, "2", "", "", "12"},
		{"compensar contributivo", entityCompensar, "1", "", "", "16"},
		{"compensar subsidiado", entityCompensar, "2", "", "", "17"},
		{"capital contributivo", entityCapital, "1", "", "", "14"},
		{"capital subsidiado", entityCapital, "2", "", "", "15"},
		// Unknown entity → empty
		{"non-eps entity", "PART02", "1", "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := resolveEPSContract(c.entity, c.regimen, c.depCode, c.muniCode)
			if got != c.want {
				t.Errorf("resolveEPSContract(%q,%q,%q,%q) = %q, want %q",
					c.entity, c.regimen, c.depCode, c.muniCode, got, c.want)
			}
		})
	}
}

func TestIsEPSWithMatrix(t *testing.T) {
	for _, e := range []string{entitySanitas, entitySaludTotal, entityCompensar, entityCapital} {
		if !isEPSWithMatrix(e) {
			t.Errorf("isEPSWithMatrix(%q) = false, want true", e)
		}
	}
	for _, e := range []string{"PART02", "EPS001", ""} {
		if isEPSWithMatrix(e) {
			t.Errorf("isEPSWithMatrix(%q) = true, want false", e)
		}
	}
}
