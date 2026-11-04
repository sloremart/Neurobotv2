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

// El parecido es simétrico y estricto: las palabras distintivas coinciden en los DOS sentidos, y
// cualquier diferencia en una de ellas (región, lateralidad, contraste, tipo de consulta, pediatría,
// vía, marcador del PET, funcional/tractografía) hace que los nombres NO se parezcan.
func TestCupsNamesMatch(t *testing.T) {
	cases := []struct {
		read, catalog string
		match         bool
	}{
		// El mismo procedimiento con otra redacción.
		{"CONSULTA PRIMERA VEZ NEUROLOGÍA", "CONSULTA DE PRIMERA VEZ POR ESPECIALISTA EN NEUROLOGIA", true},
		{"ecografia obstetrica con traslucencia nucal", "ECOGRAFIA OBSTETRICA CON TRASLUCENCIA NUCAL", true},
		{"CONSULTA OTORRINO", "CONSULTA DE PRIMERA VEZ POR ESPECIALISTA EN OTORRINOLARINGOLOGIA", false}, // falta PRIMERA VEZ
		{"RX DE RODILLA BILATERAL", "RX DE RODILLAS BILATERAL", true},                                    // plural
		// Región y lateralidad.
		{"RESONANCIA MAGNETICA DE RODILLA", "RESONANCIA MAGNETICA DE HOMBRO", false},
		{"RADIOGRAFIA DE MAXILAR SUPERIOR", "RADIOGRAFIA DE MAXILAR INFERIOR", false},
		{"RX DE RODILLA DERECHA", "RX DE RODILLA IZQUIERDA", false},
		{"RX DE RODILLA", "RX DE RODILLA BILATERAL", false},
		// Contraste, en los dos sentidos.
		{"TOMOGRAFIA DE CRANEO SIMPLE", "TOMOGRAFIA DE CRANEO CON CONTRASTE", false},
		{"TOMOGRAFIA DE CRANEO CON CONTRASTE", "TOMOGRAFIA DE CRANEO SIMPLE", false},
		{"TOMOGRAFIA DE CRANEO SIN CONTRASTE", "TOMOGRAFIA DE CRANEO CON CONTRASTE", false},
		{"RESONANCIA DE CEREBRO", "RESONANCIA DE CEREBRO CONTRASTADA", false},
		// Tipo de consulta y pediatría.
		{"CONSULTA PRIMERA VEZ NEUROLOGIA", "CONSULTA DE CONTROL O DE SEGUIMIENTO POR ESPECIALISTA EN NEUROLOGIA", false},
		{"CONSULTA PRIMERA VEZ NEUROLOGIA", "CONSULTA DE PRIMERA VEZ POR ESPECIALISTA EN NEUROLOGÍA PEDIÁTRICA", false},
		// Vía, marcador del PET, funcional o tractografía.
		{"ULTRASONOGRAFÍA DE PROSTATA TRANSRECTAL", "ULTRASONOGRAFÍA DE PROSTATA TRANSABDOMINAL  (62)", false},
		{"TOMOGRAFIA POR EMISION DE POSITRONES(PET-TC) CON 18-FLUOR", "TOMOGRAFIA POR EMISION DE POSITRONES(PET-TC) CON PSMA", false},
		{"RESONANCIA MAGNETICA DE CEREBRO FUNCIONAL", "RESONANCIA NUCLEAR MAGNETICA DE CEREBRO", false},
		{"RESONANCIA MAGNETICA DE CEREBRO CON TRACTOGRAFIA", "RESONANCIA NUCLEAR MAGNETICA DE CEREBRO", false},
		// La cantidad en el nombre también distingue (paquetes internos del catálogo).
		{"Paq. 1 electromiografia + 1 neuroconduccion", "Paq. 2 electromiografias + 2 neuroconducciones", false},
		// Sin palabras distintivas no hay con qué comparar.
		{"CONSULTA DE", "CONSULTA DE ESPECIALISTA", false},
	}
	for _, c := range cases {
		if got := CupsNamesMatch(c.read, c.catalog); got != c.match {
			t.Errorf("CupsNamesMatch(%q, %q) = %v, quería %v", c.read, c.catalog, got, c.match)
		}
		if got := CupsNamesMatch(c.catalog, c.read); got != c.match {
			t.Errorf("no es simétrico: CupsNamesMatch(%q, %q) = %v", c.catalog, c.read, got)
		}
	}
}
