package services

import "testing"

func TestCupsOneDigitApart(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"890279", "890274", true},  // sustitución
		{"890274", "890247", true},  // transposición adyacente
		{"890274", "890274", false}, // el mismo
		{"890999", "890274", false}, // tres dígitos
		{"890279", "890374", false}, // dos dígitos no adyacentes
		{"890274", "980274", true},  // transposición al inicio
		{"89027", "890274", false},  // largos distintos
		{"123", "124", false},       // demasiado corto para fiarse
	}
	for _, c := range cases {
		if got := CupsOneDigitApart(c.a, c.b); got != c.want {
			t.Errorf("CupsOneDigitApart(%s, %s) = %v, quería %v", c.a, c.b, got, c.want)
		}
	}
}

func TestCupsNameSimilarity(t *testing.T) {
	cases := []struct {
		read, catalog string
		similar       bool
	}{
		{"CONSULTA PRIMERA VEZ NEUROLOGÍA", "CONSULTA DE PRIMERA VEZ POR ESPECIALISTA EN NEUROLOGIA", true},
		{"ECOGRAFIA OBSTETRICA CON TRASLUCENCIA NUCAL", "ECOGRAFIA OBSTETRICA CON TRASLUCENCIA NUCAL", true},
		{"CONSULTA OTORRINO", "CONSULTA DE PRIMERA VEZ POR ESPECIALISTA EN OTORRINOLARINGOLOGIA", true}, // prefijo
		{"CONSULTA PRIMERA VEZ OTORRINOLARINGOLOGIA", "CONSULTA DE PRIMERA VEZ POR ESPECIALISTA EN NEUROLOGIA", false},
		{"RESONANCIA MAGNETICA DE COLUMNA LUMBAR", "RESONANCIA MAGNETICA DE CEREBRO", false},
		{"CONSULTA DE PRIMERA VEZ", "CONSULTA DE PRIMERA VEZ POR ESPECIALISTA EN NEUROLOGIA", false}, // sin palabras distintivas
	}
	for _, c := range cases {
		got := CupsNameSimilarity(c.read, c.catalog) >= CupsNameSimilarityThreshold
		if got != c.similar {
			t.Errorf("CupsNameSimilarity(%q, %q) parecido=%v, quería %v", c.read, c.catalog, got, c.similar)
		}
	}
}
