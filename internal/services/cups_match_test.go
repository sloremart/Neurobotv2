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
// vía, marcador del PET, funcional/tractografía) hace que los nombres NO se parezcan. Si coinciden solo
// por prefijo (singular/plural: MIEMBROS INFERIORES frente a MIEMBRO INFERIOR, bilateral frente a
// unilateral en CUPS) no son iguales: es duda, nunca corrección.
func TestCompareCupsNames(t *testing.T) {
	cases := []struct {
		read, catalog string
		want          CupsNameMatch
	}{
		// El mismo procedimiento con otra redacción, tildes o mayúsculas.
		{"CONSULTA PRIMERA VEZ NEUROLOGÍA", "CONSULTA DE PRIMERA VEZ POR ESPECIALISTA EN NEUROLOGIA", CupsNamesEqual},
		{"ecografia obstetrica con traslucencia nucal", "ECOGRAFIA OBSTETRICA CON TRASLUCENCIA NUCAL", CupsNamesEqual},
		// Solo por prefijo: duda.
		{"RX DE RODILLA BILATERAL", "RX DE RODILLAS BILATERAL", CupsNamesByPrefix},
		{"ECOGRAFIA DOPPLER DE VASOS VENOSOS DE MIEMBROS INFERIORES", "ECOGRAFÍA DOPPLER DE VASOS VENOSOS DE MIEMBRO INFERIOR", CupsNamesByPrefix},
		{"ECOGRAFIA DOPPLER DE VASOS VENOSOS DE MIEMBROS SUPERIORES", "ECOGRAFIA DOPPLER DE VASOS VENOSOS DE MIEMBRO SUPERIOR", CupsNamesByPrefix},
		{"CONSULTA OTORRINO", "CONSULTA DE OTORRINOLARINGOLOGIA", CupsNamesByPrefix},
		{"CONSULTA OTORRINO", "CONSULTA DE PRIMERA VEZ POR ESPECIALISTA EN OTORRINOLARINGOLOGIA", CupsNamesDiffer}, // falta PRIMERA VEZ
		// Región y lateralidad.
		{"RESONANCIA MAGNETICA DE RODILLA", "RESONANCIA MAGNETICA DE HOMBRO", CupsNamesDiffer},
		{"RADIOGRAFIA DE MAXILAR SUPERIOR", "RADIOGRAFIA DE MAXILAR INFERIOR", CupsNamesDiffer},
		{"RX DE RODILLA DERECHA", "RX DE RODILLA IZQUIERDA", CupsNamesDiffer},
		{"RX DE RODILLA", "RX DE RODILLA BILATERAL", CupsNamesDiffer},
		// Contraste, en los dos sentidos.
		{"TOMOGRAFIA DE CRANEO SIMPLE", "TOMOGRAFIA DE CRANEO CON CONTRASTE", CupsNamesDiffer},
		{"TOMOGRAFIA DE CRANEO CON CONTRASTE", "TOMOGRAFIA DE CRANEO SIMPLE", CupsNamesDiffer},
		{"TOMOGRAFIA DE CRANEO SIN CONTRASTE", "TOMOGRAFIA DE CRANEO CON CONTRASTE", CupsNamesDiffer},
		{"RESONANCIA DE CEREBRO", "RESONANCIA DE CEREBRO CONTRASTADA", CupsNamesDiffer},
		// Tipo de consulta y pediatría.
		{"CONSULTA PRIMERA VEZ NEUROLOGIA", "CONSULTA DE CONTROL O DE SEGUIMIENTO POR ESPECIALISTA EN NEUROLOGIA", CupsNamesDiffer},
		{"CONSULTA PRIMERA VEZ NEUROLOGIA", "CONSULTA DE PRIMERA VEZ POR ESPECIALISTA EN NEUROLOGÍA PEDIÁTRICA", CupsNamesDiffer},
		// Vía, marcador del PET, funcional o tractografía.
		{"ULTRASONOGRAFÍA DE PROSTATA TRANSRECTAL", "ULTRASONOGRAFÍA DE PROSTATA TRANSABDOMINAL  (62)", CupsNamesDiffer},
		{"TOMOGRAFIA POR EMISION DE POSITRONES(PET-TC) CON 18-FLUOR", "TOMOGRAFIA POR EMISION DE POSITRONES(PET-TC) CON PSMA", CupsNamesDiffer},
		{"RESONANCIA MAGNETICA DE CEREBRO FUNCIONAL", "RESONANCIA NUCLEAR MAGNETICA DE CEREBRO", CupsNamesDiffer},
		{"RESONANCIA MAGNETICA DE CEREBRO CON TRACTOGRAFIA", "RESONANCIA NUCLEAR MAGNETICA DE CEREBRO", CupsNamesDiffer},
		// La cantidad en el nombre también distingue (paquetes internos del catálogo).
		{"Paq. 1 electromiografia + 1 neuroconduccion", "Paq. 2 electromiografias + 2 neuroconducciones", CupsNamesDiffer},
		// Sin palabras distintivas no hay con qué comparar.
		{"CONSULTA DE", "CONSULTA DE ESPECIALISTA", CupsNamesDiffer},
	}
	for _, c := range cases {
		if got := CompareCupsNames(c.read, c.catalog); got != c.want {
			t.Errorf("CompareCupsNames(%q, %q) = %v, quería %v", c.read, c.catalog, got, c.want)
		}
		if got := CompareCupsNames(c.catalog, c.read); got != c.want {
			t.Errorf("no es simétrico: CompareCupsNames(%q, %q) = %v", c.catalog, c.read, got)
		}
	}
}
