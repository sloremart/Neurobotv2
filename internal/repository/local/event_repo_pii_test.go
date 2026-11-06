package local

import (
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
