package services

import "testing"

// Lo que el catálogo no distingue no es duda al asignar: 873444 es solo de extremidades, así que las oblicuas de
// una columna no cambian de código; y los potenciales se facturan por extremidad como la EMG. Al descartar una
// lectura sí se comparan.
func TestResolveCupsByName_WhatTheCatalogDoesNotDistinguish(t *testing.T) {
	catalog := []CupsCandidate{
		{"871040", "RADIOGRAFIA DE COLUMNA LUMBOSACRA"},
		{"873444", "RADIOGRAFÍAS EN EXTREMIDADES PROYECCIONES ADICIONALES: STRESS, TUNEL, OBLICUAS  (54)"},
		{"891530", "POTENCIALES EVOCADOS SOMATOSENSORIALES (275)"},
	}
	for read, want := range map[string]string{
		"RX COLUMNA LUMBAR OBLICUAS":                          "871040",
		"RX COLUMNA LUMBOSACRA AP LAT Y OBLICUAS":             "871040",
		"POTENCIALES EVOCADOS SOMATOSENSORIALES EXTREMIDADES": "891530",
	} {
		got := acceptedByAdversary(read, catalog, false)
		if len(got) == 0 || got[0] != want {
			t.Errorf("%q: %v, quiero %s", read, got, want)
		}
	}
	if got := compareReadNames("RX COLUMNA LUMBAR OBLICUAS", "RX COLUMNA LUMBAR"); got == CupsNamesEqual {
		t.Error("al descartar, las oblicuas no se pierden")
	}
}

// Revisión del lote C, ronda 3 (R3-M2): el ruido de la orden se aplicaba fuera de su caso.
func TestCompareReadNames_NoiseOnlyInItsCase(t *testing.T) {
	cases := []struct {
		name, a, b string
		want       CupsNameMatch
	}{
		// El catálogo tiene código propio para las proyecciones adicionales (873444).
		{"oblicuas", "RX RODILLA OBLICUAS", "RX RODILLA", CupsNamesDiffer},
		{"oblicua frente a lateral", "RX RODILLA LATERAL", "RX RODILLA OBLICUA", CupsNamesDiffer},
		{"oblicuas en columna", "RX COLUMNA LUMBAR OBLICUAS", "RX COLUMNA LUMBAR AP Y LATERAL", CupsNamesByPrefix},
		{"proyecciones al descartar", "RX RODILLA PROYECCIONES", "RX RODILLA", CupsNamesByPrefix},
		// La región en silencio es duda, también donde EXTREMIDAD era ruido.
		{"doppler", "ECO DOPPLER", "ECO DOPPLER MIEMBROS", CupsNamesByPrefix},
		{"angiotac", "ANGIOTAC", "ANGIOTAC MIEMBRO", CupsNamesByPrefix},
		{"radiografía", "RX", "RX EXTREMIDADES", CupsNamesByPrefix},
		{"tomografía", "TAC", "TAC EXTREMIDADES", CupsNamesByPrefix},
		{"resonancia", "RM", "RM MIEMBROS", CupsNamesByPrefix},
		{"EMG", "EMG EXTREMIDADES", "EMG", CupsNamesByPrefix},
		{"NC", "NC EXTREMIDADES", "NC", CupsNamesByPrefix},
		// Unilateral frente a bilateral es duda con o sin una palabra de ruido de más.
		{"prefijo", "EMG MIEMBRO", "EMG MIEMBROS", CupsNamesByPrefix},
		{"prefijo con ruido", "EMG MIEMBRO URGENTE", "EMG MIEMBROS", CupsNamesByPrefix},
		// Solo el prefijo los acerca: candidateFits no encaja, porque CONTRASTES no es un valor del grupo.
		{"prefijo sin encajar", "TAC CRANEO CONTRASTES", "TAC CRANEO CONTRASTE", CupsNamesByPrefix},
		{"prefijo con barra sin entender", "TAC C/X RODILLA", "TAC C/X RODILLAS", CupsNamesByPrefix},
		{"prefijo con ruido, una extremidad", "EMG UNA EXTREMIDAD URGENTE", "EMG EXTREMIDADES", CupsNamesByPrefix},
		// Lo que sí es ruido.
		{"prioridad", "RX TORAX URGENTE", "RX TORAX", CupsNamesEqual},
		{"lateral", "RX RODILLA AP Y LATERAL", "RX RODILLA", CupsNamesEqual},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := compareReadNames(c.a, c.b); got != c.want {
				t.Errorf("compareReadNames(%q, %q) = %d, quiero %d", c.a, c.b, got, c.want)
			}
			if got := compareReadNames(c.b, c.a); got != c.want {
				t.Errorf("compareReadNames(%q, %q) = %d, quiero %d", c.b, c.a, got, c.want)
			}
		})
	}
}

func TestResolveCupsByName_NoiseOnlyInItsCase(t *testing.T) {
	catalog := []CupsCandidate{
		{"873420", "RADIOGRAFIA DE RODILLA AP, LATERAL"},
		{"873444", "RADIOGRAFÍAS EN EXTREMIDADES PROYECCIONES ADICIONALES: STRESS, TUNEL, OBLICUAS  (54)"},
		{"873123", "RADIOGRAFIAS COMPARATIVAS DE EXTREMIDADES SUPERIORES"},
		{"1005927", "RADIOGRAFIA COMPARATIVA"},
		{"930860", "ELECTROMIOGRAFÍIA EN CADA EXTREMIDAD (UNO O MAS MUSCULOS)"},
		{"891509", "NEUROCONDUCCION (CADA NERVIO)"},
		{"882282", "ECOGRAFÍA DOPPLER DE VASOS ESCROTALES"},
		{"881511", "ULTRASONOGRAFÍA TESTICULAR CON ANALISIS DOPPLER"},
	}
	// Van al agente con candidatos: ni se asignan ni se descartan.
	for _, read := range []string{"RX RODILLA OBLICUAS", "RX PROYECCIONES OBLICUAS", "RX RODILLA PROYECCIONES ADICIONALES", "DOPPLER TESTICULAR", "ECO DOPPLER TESTICULAR"} {
		if got := acceptedByAdversary(read, catalog, false); len(got) != 0 {
			t.Errorf("%q: se asignaría %v", read, got)
		}
		if res := ResolveCupsByName(read, catalog, false, nil); len(res.Candidates) == 0 {
			t.Errorf("%q debe ir al agente con candidatos", read)
		}
	}
	// Las extremidades de una radiografía comparativa no son la radiografía comparativa a secas.
	for _, read := range []string{"RX COMPARATIVAS DE EXTREMIDADES SUPERIORES", "RX COMPARATIVAS EXTREMIDADES SUPERIORES"} {
		if res := ResolveCupsByName(read, catalog, false, nil); res.Code != "873123" {
			t.Errorf("%q: %+v, quiero 873123 por nombre igual", read, res)
		}
	}
	// Singular y plural valen lo mismo; superiores e inferiores nunca.
	for _, read := range []string{"RX COMPARATIVA DE EXTREMIDADES SUPERIORES", "RADIOGRAFIA COMPARATIVA EXTREMIDADES SUPERIORES", "RX COMPARATIVAS MMSS"} {
		if res := ResolveCupsByName(read, catalog, false, nil); res.Code != "873123" {
			t.Errorf("%q: %+v, quiero 873123", read, res)
		}
	}
	if res := ResolveCupsByName("RX COMPARATIVAS", catalog, false, nil); res.Code != "1005927" {
		t.Errorf("la comparativa a secas: %+v", res)
	}
	for _, p := range [][2]string{
		{"RADIOGRAFIAS COMPARATIVAS DE EXTREMIDADES SUPERIORES", "RADIOGRAFIAS COMPARATIVAS DE EXTREMIDADES INFERIORES"},
		{"RX COMPARATIVAS EXTREMIDADES SUPERIORES", "RX COMPARATIVA EXTREMIDADES INFERIORES"},
		{"RX COMPARATIVAS MMSS", "RX COMPARATIVAS MMII"},
	} {
		if got := compareReadNames(p[0], p[1]); got != CupsNamesDiffer {
			t.Errorf("compareReadNames(%q, %q) = %d", p[0], p[1], got)
		}
		if got := CompareCupsNames(canonicalOrderName(p[0]), canonicalOrderName(p[1])); got != CupsNamesDiffer {
			t.Errorf("CompareCupsNames(%q, %q) = %d", p[0], p[1], got)
		}
	}
	// En la EMG y la NC las extremidades siguen siendo solo dónde se hacen.
	// «3 PROYECCIONES» cuenta vistas, que el catálogo no distingue: al asignar sigue siendo ruido.
	for read, want := range map[string]string{
		"RX RODILLA 2 PROYECCIONES": "873420", "RX RODILLA 3 PROYECCIONES": "873420",
		"EMG EXTREMIDADES": "930860", "EMG 4 EXTREMIDADES": "930860", "EMG MMSS": "930860", "EMG MIEMBRO URGENTE": "930860",
		"NC EXTREMIDADES": "891509", "NC 4 EXTREMIDADES": "891509", "NC MMII": "891509", "NEUROCONDUCCION MIEMBROS INFERIORES": "891509",
	} {
		got := acceptedByAdversary(read, catalog, false)
		if len(got) == 0 || got[0] != want {
			t.Errorf("%q: %v, quiero %s", read, got, want)
		}
	}
}
