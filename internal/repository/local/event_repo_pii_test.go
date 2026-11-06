package local

import (
	"strings"
	"testing"
	"time"
)

// C-04: del lote leído en orden de id se enmascara el tramo inicial que ya pasó la retención; la
// primera fila reciente corta el lote (las de después quedan para la pasada siguiente).
func TestPIIMaskRange(t *testing.T) {
	cutoff := time.Date(2026, 10, 7, 2, 30, 0, 0, time.UTC)
	old := func(minutes int) time.Time { return cutoff.Add(-time.Duration(minutes) * time.Minute) }
	cases := []struct {
		name      string
		rows      []chatEventStamp
		wantUpTo  int64
		wantUntil time.Time
	}{
		{"lote vacío", nil, 0, time.Time{}},
		{"todo viejo", []chatEventStamp{{10, old(30)}, {11, old(20)}, {13, old(10)}}, 13, old(10)},
		{"corta en la primera reciente", []chatEventStamp{{10, old(30)}, {11, cutoff}, {12, old(5)}}, 10, old(30)},
		{"la primera ya es reciente", []chatEventStamp{{10, cutoff.Add(time.Minute)}, {11, old(5)}}, 0, time.Time{}},
		{"created_at fuera de orden dentro del tramo", []chatEventStamp{{10, old(5)}, {11, old(9)}}, 11, old(5)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			upTo, until := piiMaskRange(c.rows, cutoff)
			if upTo != c.wantUpTo || !until.Equal(c.wantUntil) {
				t.Fatalf("piiMaskRange = (%d, %v), want (%d, %v)", upTo, until, c.wantUpTo, c.wantUntil)
			}
		})
	}
}

// La pasada siguiente arranca una hora antes de la marca: el id y el created_at no van en el mismo
// orden exacto y una fila vieja con id posterior al corte de un lote queda para la noche siguiente.
func TestPIIMaskSince_StartsAnHourBeforeTheMark(t *testing.T) {
	mark := time.Date(2026, 10, 7, 2, 30, 0, 0, time.UTC)
	if got := piiMaskSince(mark); !got.Equal(mark.Add(-time.Hour)) {
		t.Fatalf("piiMaskSince = %v, want %v", got, mark.Add(-time.Hour))
	}
}

// Forma del UPDATE de la retención. Lo que MySQL hace con él (JSON_REMOVE, CASE, LIKE) solo lo
// comprueba el test de integración (event_repo_pii_integration_test.go, make test-integration).
func TestMaskPIISQL_Shape(t *testing.T) {
	for _, want := range []string{
		"WHERE id > ? AND id <= ? AND created_at < ?", // nunca toca una fila reciente del rango
		"phone_number = CASE WHEN phone_number LIKE 'nectar:%'",
		"contact_identifier = CASE WHEN contact_identifier LIKE 'nectar:%'",
		"LIKE '***%'",                           // idempotente: lo ya enmascarado no se vuelve a cortar
		"CONCAT('***', RIGHT(phone_number, 4))", // como mucho los últimos 4
		"event_type = 'registration_failed'",    // su error puede traer valores del paciente
		"'$.error'",
	} {
		if !strings.Contains(maskPIISQL, want) {
			t.Errorf("el UPDATE no contiene %q:\n%s", want, maskPIISQL)
		}
	}
	for _, key := range []string{"name", "email", "phone", "text", "input", "last_input", "doc", "document", "query", "description"} {
		if !strings.Contains(maskPIISQL, "'$."+key+"'") {
			t.Errorf("el UPDATE no quita la clave %q", key)
		}
	}
	// El error de los demás eventos (ocr_failed lo lee el dashboard) se conserva.
	if strings.Count(maskPIISQL, "'$.error'") != 1 {
		t.Errorf("'$.error' solo se quita de registration_failed:\n%s", maskPIISQL)
	}
}
