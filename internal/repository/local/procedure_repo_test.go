package local

import "testing"

// TestServiceNameForSubjectType verifica el mapa asunto→servicio (Capa 1 de la clasificación),
// que reemplazó la columna Antares `servicio`. forceServiceByCode (en services) sobreescribe
// para códigos específicos; este mapa es la fuente base por asunto.
func TestServiceNameForSubjectType(t *testing.T) {
	cases := map[int]string{
		1: "Fisiatria", 7: "Fisiatria", 15: "Fisiatria",
		2:  "Radiografia",
		3:  "Tomografia",
		12: "PET", // PET/CT: servicio propio (antes compartía "Tomografia" con asunto 3)
		4:  "Resonancia",
		17: "Resonancia",
		6:  "Ecografia",
		19: "Ecografia",
		8:  "Neurologia",
		9:  "Neurologia",
		10: "Neurologia",
		11: "Neurologia",
		16: "Neurologia",
		5:  "General",
		13: "General",
		14: "General",
		0:  "General",
		99: "General",
	}
	for subjectType, want := range cases {
		if got := serviceNameForSubjectType(subjectType); got != want {
			t.Errorf("serviceNameForSubjectType(%d) = %q, want %q", subjectType, got, want)
		}
	}
}
