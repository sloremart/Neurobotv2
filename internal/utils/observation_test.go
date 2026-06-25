package utils

import "testing"

func TestObservationHasContrast(t *testing.T) {
	cases := []struct {
		obs  string
		want bool
	}{
		{"Contrastada", true},
		{"CONTRASTADO", true},
		{"CONTRASTE", true},
		{"contrastada", true},
		{"RESONANCIA CONTRASTADA-30 MIN ANTES", true},
		{"angiotac de torax contrastada", true},
		{"Contrastada, Bajo Sedación", true},
		{"Creat 05 de mayo/CONTRASTADA", true},
		// Negaciones → no contrastado
		{"sin contraste", false},
		{"no contrastada", false},
		{"RM simple sin medio de contraste", false},
		// Sin mención de contraste
		{"simple", false},
		{"RM de rodilla", false},
		{"", false},
	}
	for _, c := range cases {
		if got := ObservationHasContrast(c.obs); got != c.want {
			t.Errorf("ObservationHasContrast(%q) = %v, want %v", c.obs, got, c.want)
		}
	}
}

func TestObservationHasSedation(t *testing.T) {
	cases := []struct {
		obs  string
		want bool
	}{
		{"Bajo Sedación", true},
		{"bajo sedacion", true},
		{"BAJO SEDACION", true},
		{"con anestesia general", true},
		{"paciente sedado", true},
		{"Contrastada, Bajo Sedación", true},
		{"sin sedación", false},
		{"no sedacion", false},
		{"RM de rodilla", false},
		{"", false},
	}
	for _, c := range cases {
		if got := ObservationHasSedation(c.obs); got != c.want {
			t.Errorf("ObservationHasSedation(%q) = %v, want %v", c.obs, got, c.want)
		}
	}
}
