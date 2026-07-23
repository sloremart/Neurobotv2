package handlers

import (
	"strings"
	"testing"
)

// El mensaje de fallo del OCR debe ser ACCIONABLE según la causa (§5e.2): borrosa → "buena luz/enfoque";
// tabla no detectada → "fotografía los códigos/CUPS"; formato → "JPG o PDF"; y un genérico con consejos.
func TestOCRFailureMessage_PerType(t *testing.T) {
	cases := []struct {
		reason string
		must   string // substring esperado (en minúsculas)
	}{
		{"imagen_borrosa", "borrosa"},
		{"no_table_detected", "códigos"},
		{"formato_no_soportado", "pdf"},
		{"", "consejo"},                 // genérico con consejos
		{"algo_desconocido", "consejo"}, // causa no mapeada → genérico
	}
	for _, c := range cases {
		got := strings.ToLower(ocrFailureMessage(c.reason))
		if !strings.Contains(got, c.must) {
			t.Errorf("ocrFailureMessage(%q) = %q; esperaba que contuviera %q", c.reason, got, c.must)
		}
	}
	// El genérico y los específicos deben diferir (no todos el mismo texto).
	if ocrFailureMessage("imagen_borrosa") == ocrFailureMessage("") {
		t.Error("el mensaje de borrosa no debe ser igual al genérico")
	}
}
