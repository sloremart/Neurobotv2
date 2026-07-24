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

func TestAppointmentIsContrasted(t *testing.T) {
	cases := []struct {
		name  string
		obs   string
		names []string
		want  bool
	}{
		// La observación manda cuando existe.
		{"obs contrastada", "Contrastada", []string{"TAC DE CRANEO"}, true},
		{"obs negada gana al nombre ambiguo", "sin contraste", []string{"TAC DE CRANEO"}, false},
		// Sin señal en la observación, el NOMBRE del CUP resuelve (cita de agente).
		{"nombre con contraste", "", []string{"TAC DE CRANEO CON CONTRASTE"}, true},
		{"nombre simple y con contraste", "", []string{"TAC DE CRANEO SIMPLE Y CON CONTRASTE"}, true},
		{"nombre con contraste + acentos/caso", "creat 05 may", []string{"Rnm De Columna Con Contraste"}, true},
		// Nombres que NO deben marcar contraste.
		{"nombre simple", "", []string{"TAC DE CRANEO SIMPLE"}, false},
		{"nombre sin contraste", "", []string{"TAC DE ABDOMEN SIN CONTRASTE"}, false},
		{"nombre neutro", "", []string{"RNM DE RODILLA"}, false},
		{"sin datos", "", nil, false},
		// Multi-CUP: basta que UN nombre sea contrastado.
		{"un cup contrastado del grupo", "", []string{"TAC DE TORAX", "TAC DE ABDOMEN CON CONTRASTE"}, true},
	}
	for _, c := range cases {
		if got := AppointmentIsContrasted(c.obs, c.names...); got != c.want {
			t.Errorf("%s: AppointmentIsContrasted(%q, %v) = %v, want %v", c.name, c.obs, c.names, got, c.want)
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
