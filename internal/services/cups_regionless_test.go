package services

import "testing"

// Revisión del lote C, ronda 4 (R4-I3, decisión del dueño): una lectura SIN región frente a un catálogo con
// varias filas que son ese mismo procedimiento CON región es duda, aunque exista la fila a secas. «RX
// COMPARATIVAS» puede ser cualquiera de las siete comparativas con región: al agente.
func TestResolveCupsByName_RegionlessReadWithRegionalRowsIsADoubt(t *testing.T) {
	catalog := []CupsCandidate{
		{"1005927", "RADIOGRAFIA COMPARATIVA"},
		{"873123", "RADIOGRAFIAS COMPARATIVAS DE EXTREMIDADES SUPERIORES"},
		{"873443", "RADIOGRAFIAS COMPARATIVAS DE EXTREMIDADES INFERIORES  (54)"},
		{"873412", "RADIOGRAFIA DE PELVIS (CADERA) COMPARATIVA (54)"},
		{"873422", "RADIOGRAFIA DE RODILLAS COMPARATIVAS POSICION VERTICAL (UNICAMENTE VISTA ANTEROPOSTERIOR)  (54)"},
		{"873202", "RADIOGRAFIA DE ARTICULACIONES ACROMIO CLAVICULARES COMPARATIVAS  (54)"},
		{"873303", "RADIOGRAFÍA COMPARATIVA DE PIES CON APOYO (AP Y\nLATERAL)"},
		{"870005", "RADIOGRAFIA DE MASTOIDES COMPARATIVAS"},
		{"873444", "RADIOGRAFÍAS EN EXTREMIDADES PROYECCIONES ADICIONALES: STRESS, TUNEL, OBLICUAS  (54)"},
		{"930860", "ELECTROMIOGRAFÍIA EN CADA EXTREMIDAD (UNO O MAS MUSCULOS)"},
		{"930820", "ELECTROMIOGRAFÍA EN CARA"},
	}
	for _, read := range []string{
		"RX COMPARATIVAS", "RX COMPARATIVA", "RADIOGRAFIA COMPARATIVA", "RX COMPARATIVAS DE", "RX COMPARATIVAS DE:",
		"RX COMPARATIVAS #2", "RX COMPARATIVAS X2", "RX COMPARATIVAS L4", "RX COMPARATIVAS BILATERAL", "RX COMPARATIVA D",
		"RX COMPARATIVAS (54)", "RX COMPARATIVAS AP Y LATERAL", "RX COMPARATIVAS URGENTE",
	} {
		if res := ResolveCupsByName(read, catalog, false, nil); res.Code != "" {
			t.Errorf("%q: se asignaría %s por nombre igual", read, res.Code)
		}
		if got := acceptedByAdversary(read, catalog, false); len(got) != 0 {
			t.Errorf("%q: se asignaría %v", read, got)
		}
		if res := ResolveCupsByName(read, catalog, false, nil); len(res.Candidates) == 0 {
			t.Errorf("%q debe ir al agente con candidatos", read)
		}
	}
	// Con la región (o con lo que la distingue) se sigue asignando. («RX RODILLAS COMPARATIVAS», «RX PELVIS
	// COMPARATIVA» y «RX COMPARATIVA DE PIES» ya iban al agente: su fila trae POSICION VERTICAL, CADERA o APOYO.)
	for read, want := range map[string]string{
		"RX COMPARATIVAS MMSS": "873123", "RX MASTOIDES COMPARATIVAS": "870005",
		"RX EXTREMIDADES PROYECCIONES ADICIONALES STRESS TUNEL OBLICUAS": "873444",
	} {
		got := acceptedByAdversary(read, catalog, false)
		if len(got) != 1 || got[0] != want {
			t.Errorf("%q: %v, quiero %s", read, got, want)
		}
	}
	// Una sola fila con región no es «varias»: la EMG a secas sigue siendo la de cada extremidad.
	if got := acceptedByAdversary("EMG", catalog, false); len(got) != 1 || got[0] != "930860" {
		t.Errorf("EMG: %v", got)
	}
}

// Ronda 5 (R5-I2): solo cuentan las filas que son el leído MÁS una región, sin lo que el catálogo pone entre
// paréntesis y sin otra anatomía: «RX MIEMBRO INFERIOR AP Y LATERAL» es el nombre exacto de 873340 y se asigna;
// «MAXILAR INFERIOR» (870113) y «MEDICION DE MIEMBROS INFERIORES … (PIES CON APOYO)» (873302) no son «RX de
// miembro inferior con región». «RX COMPARATIVAS» sigue en duda con el catálogo entero (PELVIS y MASTOIDES).
func TestResolveCupsByName_RegionalRowsAreTheReadPlusARegion(t *testing.T) {
	catalog := catalogFromTSV(t, "testdata/cups_catalog_active.tsv")
	if res := ResolveCupsByName("RX MIEMBRO INFERIOR AP Y LATERAL", catalog, false, nil); res.Code != "873340" {
		t.Errorf("«RX MIEMBRO INFERIOR AP Y LATERAL» → %q, quiero 873340 por nombre igual", res.Code)
	}
	for _, read := range []string{
		"RX MIEMBRO INFERIOR", "RX MIEMBROS INFERIORES", "RX MMII AP Y LATERAL", "RX DE MIEMBRO INFERIOR IZQUIERDO",
		"RX MIEMBRO INFERIOR DERECHO AP Y LATERAL",
	} {
		if got := acceptedByAdversary(read, catalog, false); len(got) != 1 || got[0] != "873340" {
			t.Errorf("%q → %v, quiero [873340]", read, got)
		}
	}
	// Un calificador que el leído sí dice (PERFUSION) cuenta: «RM PERFUSION» no es «RM <región>» con región.
	for read, want := range map[string]string{"RM PERFUSION": "883904", "TAC PERFUSION": "879903"} {
		if res := ResolveCupsByName(read, catalog, false, nil); res.Code != want {
			t.Errorf("%q → %q, quiero %s por nombre igual", read, res.Code, want)
		}
	}
	for _, read := range []string{"RX COMPARATIVAS", "RX COMPARATIVA", "RX COMPARATIVAS AP Y LATERAL"} {
		if got := acceptedByAdversary(read, catalog, false); len(got) != 0 {
			t.Errorf("%q → %v, quiero agente", read, got)
		}
		if res := ResolveCupsByName(read, catalog, false, nil); len(res.Candidates) == 0 {
			t.Errorf("%q debe ir al agente con candidatos", read)
		}
	}
}
