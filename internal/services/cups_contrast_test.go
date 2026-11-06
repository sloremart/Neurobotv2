package services

import "testing"

// Revisión del lote C, ronda 4 (R4-I2): la lectura del contraste está cerrada a fallos. Esta tabla ES la
// gramática: lo que no está aquí como SIMPLE, CONTRASTE o AMBOS es DESCONOCIDO.
func TestReadContrast_Grammar(t *testing.T) {
	cases := map[contrastReading][]string{
		contrastSilent: {
			"TAC CRANEO", "RX RODILLA AP Y LATERAL", "RM MIEMBRO SUPERIOR SIN INCLUIR ARTICULACIONES",
			"ANGIORRESONANCIA DE TORAX (SIN INCLUIR CORAZON)", "TOMOGRAFIA POR EMISION DE POSITRONES(PET-TC) CON 18-FLUOR",
			"ULTRASONOGRAFIA DE TIROIDES CON TRANSDUCTOR DE 7 MHZ O MAS", "POTENCIALES EVOCADOS VISUALES (UNI O BILATERALES)",
			"COLUMNA SEGMENTOS CERVICAL, TORACICO, LUMBAR Y/O SACRO", "RADIOGRAFIA DE TORAX (OBLICUAS Ó LATERAL CON BARIO)",
			"RX PIE CON APOYO", "TAC OIDO MEDIO", "EMG/NC", "PET/CT", "CONSULTA NO CONTROL NEUROLOGIA", "EMG NO NC",
			"", "RX RODILLA D.",
		},
		contrastSimple: {
			"TAC CRANEO SIMPLE", "RADIOGRAFIA DE ABDOMEN SIMPLE", "TAC CRANEO SIN CONTRASTE", "SIN MEDIO DE CONTRASTE",
			"SIN MEDIO CONTRASTE", "SIN GADOLINIO", "SIN CONTRASTADO", "NO CONTRASTADO", "NO CONTRASTADA", "NO CONTRASTE",
			"NO CON CONTRASTE", "NO MEDIO DE CONTRASTE", "NO CON MEDIO DE CONTRASTE", "NO GADOLINIO", "NO C/C",
			"S/C", "S/CONTRASTE", "S/ MEDIO DE CONTRASTE", "S / C", "S/MEDIO CONTRASTE", "SIN CTE",
			"CONTRASTE: NO", "CONTRASTE NO", "CONTRASTADO (NO)", "CON CONTRASTE: NO",
			// Confusiones del OCR y espacios: N0 por NO, C0 por CO, 5 por S, saltos de línea y dobles espacios.
			"N0 CONTRASTADO", "N 0 CONTRASTE", "CONTRASTE: N0", "SIN C0NTRASTE", "5IN CONTRASTE", "SIN  CONTRASTE",
			"NO\nCONTRASTADO", "SIN-CONTRASTE", "tac craneo no contrastado",
			// Lo de otros procedimientos del nombre no cambia la lectura.
			"TAC CRANEO SIMPLE Y CUELLO", "TAC SIN CONTRASTE DE CRANEO", "RX ABDOMEN SIMPLE Y DE PIE",
		},
		contrastWith: {
			"CON CONTRASTE", "CONTRASTADO", "CONTRASTADA", "CONTRASTE", "C/C", "C/CTE", "C/ CONTRASTE", "C/CONTRASTE",
			"C/MEDIO DE CONTRASTE", "CON MEDIO DE CONTRASTE", "CON MEDIO CONTRASTE", "CON GADOLINIO", "GADOLINIO",
			"CON CTE", "C0N CONTRASTE", "CON C0NTRASTE", "CONTRASTE: SI", "CONTRASTE SI", "C / C",
			"TAC CON CONTRASTE DE CRANEO", "TAC CRANEO Y CUELLO CON CONTRASTE",
		},
		contrastBoth: {
			"CON Y SIN CONTRASTE", "SIN Y CON CONTRASTE", "CON O SIN CONTRASTE", "SIN O CON CONTRASTE",
			"CON/SIN CONTRASTE", "CON / SIN CONTRASTE", "SIN/CON CONTRASTE", "C/S CONTRASTE", "C / S CONTRASTE",
			"C/S MEDIO DE CONTRASTE", "C/S GADOLINIO", "CON Y SIN MEDIO DE CONTRASTE", "SIN Y CON MEDIO DE CONTRASTE",
			"CON/SIN MEDIO DE CONTRASTE", "CON / SIN MEDIO DE CONTRASTE", "CON O SIN MEDIO DE CONTRASTE",
			"CON Y SIN GADOLINIO", "SIN/CON GADOLINIO", "CON Y SIN CONTRASTADO", "SIN Y CON CONTRASTADO",
			"CON Y SIN MEDIO CONTRASTE", "CON Y  SIN CONTRASTE", "CON Y SIN\nCONTRASTE", "CON  O SIN CONTRASTE",
			"CON O SIN\nCONTRASTE", "SIN CONTRASTE O CON CONTRASTE", "CON CONTRASTE O SIN CONTRASTE",
			"SIN CONTRASTE Y CON CONTRASTE", "CON CONTRASTE Y SIN CONTRASTE", "C/C Y S/C", "S/C Y C/C", "C/C O S/C",
			"SIMPLE Y CON CONTRASTE", "SIMPLE Y CONTRASTADO", "SIMPLE Y CONTRASTADA", "SIMPLE Y CONTRASTE",
			"SIMPLE + CONTRASTE", "SIMPLE Y CON MEDIO DE CONTRASTE",
			"TOMOGRAFIA AXIAL COMPUTADA DE CRANEO SIMPLE Y CON CONTRASTE",
		},
		contrastUnknown: {
			// Abierto o con un orden que no está en la gramática.
			"SIMPLE O CONTRASTADO", "CONTRASTE O SIMPLE", "CONTRASTADO O SIMPLE", "SIMPLE O CON CONTRASTE",
			"CON Y/O SIN CONTRASTE", "SIMPLE Y/O CONTRASTE", "CONTRASTE Y/O SIMPLE", "CONTRASTADO O NO",
			"CON CONTRASTE O NO", "CON/O SIN CONTRASTE", "SIMPLE CONTRASTE",
			"SIMPLE-CONTRASTE", "CON-SIN CONTRASTE", "S/C C/C", "SIMPLE CON CONTRASTE", "NO CON C/C",
			// Cortado o sin objeto.
			"TAC CRANEO CON", "TAC CRANEO (CON)", "TAC CRANEO SIN", "CON Y SIN", "C/S", "CON O", "CON O SIN",
			"TAC CRANEO SIMPLE Y", "TAC CRANEO CON CONTRASTE Y", "TAC CRANEO CON MEDIO", "TAC CRANEO SIN MEDIO",
			// Barra o sigla que no se entiende.
			"C/X", "X/C", "TAC C/X CRANEO", "CONTRASTE N/A", "S.C.", "C.C.", "CONTRASTE?",
			// Palabra parecida a CONTRASTE pero que no es la de la gramática.
			"CONTRASTES", "CONTRAST", "CONTRATE", "CONTRASE", "CONSTRASTE", "NOCONTRASTADO", "SIN CONTRAS",
			// Negado de otra forma: lo que dice no está en la gramática.
			"CONTRASTE NEGATIVO", "NI CONTRASTE", "NO SE REQUIERE CONTRASTE", "NO REQUIERE CONTRASTE", "NO USAR CONTRASTE",
			"NO APLICA CONTRASTE", "CONTRASTE NO APLICA", "NO HAY CONTRASTE", "NO LLEVA CONTRASTE", "NO 1 CONTRASTE",
			"SIN USO DE CONTRASTE", "RM CEREBRO NO FUNCIONAL CON CONTRASTE", "CON CONTRASTE NO IONICO",
			// Dos lecturas distintas en el mismo nombre.
			"TAC CRANEO SIMPLE Y TAC TORAX CON CONTRASTE", "SIMPLE Y CON CONTRASTE SIN CONTRASTE",
		},
	}
	for want, texts := range cases {
		for _, text := range texts {
			if got, _ := readContrast(foldProcedureText(text)); got != want {
				t.Errorf("readContrast(%q) = %q, quiero %q", text, got, want)
			}
		}
	}
}

// Lo que el clasificador deja en el nombre: la frase entera se reemplaza por su valor, y lo demás queda.
func TestReadContrast_ReplacesThePhrase(t *testing.T) {
	cases := map[string]string{
		"TAC CRANEO CON Y SIN MEDIO DE CONTRASTE": "TAC CRANEO SIMPLEYCONTRASTE",
		"TAC CON CONTRASTE DE CRANEO":             "TAC CONTRASTE DE CRANEO",
		"TAC CRANEO SIMPLE Y CUELLO":              "TAC CRANEO SIMPLE Y CUELLO",
		"TAC CRANEO C/S":                          "TAC CRANEO CONTRASTEDUDOSO",
		"TAC CRANEO CONTRASTE N/A":                "TAC CRANEO CONTRASTEDUDOSO",
		"TAC CRANEO SIMPLE Y TAC TORAX C/C":       "TAC CRANEO CONTRASTEDUDOSO Y TAC TORAX CONTRASTEDUDOSO",
		"RM CEREBRO NO FUNCIONAL CON CONTRASTE":   "RM CEREBRO NO FUNCIONAL CONTRASTEDUDOSO",
		"TAC CRANEO":                              "TAC CRANEO",
	}
	for text, want := range cases {
		if _, got := readContrast(foldProcedureText(text)); got != want {
			t.Errorf("readContrast(%q) deja %q, quiero %q", text, got, want)
		}
	}
	// La forma canónica es estable: volver a leerla no cambia nada.
	for _, text := range []string{
		"TAC CRANEO CON Y SIN CONTRASTE", "TAC CRANEO C/X", "TAC CRANEO SIMPLE", "RM CEREBRO CONTRASTADA", "TAC CRANEO",
	} {
		canon := canonicalOrderName(text)
		if again := canonicalOrderName(canon); again != canon {
			t.Errorf("canonicalOrderName no es idempotente: %q → %q → %q", text, canon, again)
		}
	}
}

// Las palabras que no son del contraste se normalizan igual: letras con punto, cantidades, barras y confusiones.
func TestFoldProcedureText_Normalization(t *testing.T) {
	cases := map[string]string{
		"RX RODILLA M.D.":          "RX RODILLA MD",
		"RX TORAX P.A. Y LATERAL":  "RX TORAX PA Y LATERAL",
		"RX COLUMNA N0 CERVICAL":   "RX COLUMNA NO CERVICAL",
		"PET CT N0 PSMA":           "PET CT NO PSMA",
		"RX TORAX No. 2":           "RX TORAX",
		"RX TORAX N 2":             "RX TORAX",
		"RX TORAX #2":              "RX TORAX",
		"RX TORAX NO 2":            "RX TORAX NO 2",
		"TAC   CRANEO\n C / S":     "TAC CRANEO C/S",
		"PAQ. 4 EMG + 2 NC":        "PAQ 4 EMG Y 2 NC",
		"RM COLUMNA LUMBAR L4-L5":  "RM COLUMNA LUMBAR L4 L5",
		"Resonancia de cráneo (1)": "RESONANCIA DE CRANEO 1",
	}
	for text, want := range cases {
		if got := foldProcedureText(text); got != want {
			t.Errorf("foldProcedureText(%q) = %q, quiero %q", text, got, want)
		}
	}
}
