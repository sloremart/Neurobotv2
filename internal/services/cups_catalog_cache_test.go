package services

import (
	"slices"
	"sync"
	"testing"
)

// Ronda 6c: las formas canónicas del catálogo se calculan una vez y se leen desde varias goroutines (una por
// orden en VALIDATE_OCR). Lo cacheado es lo mismo que lo calculado, es estable entre llamadas y no se muta.
func TestCatalogRowCache_IsStableAndSafe(t *testing.T) {
	catalog := catalogFromTSV(t, "testdata/cups_catalog_active.tsv")
	for _, c := range catalog {
		fresh, cached := newCatalogRow(c.Name), rowOf(c.Name)
		if fresh.canon != cached.canon || fresh.core != cached.core ||
			!slices.Equal(fresh.words, cached.words) || !slices.Equal(fresh.coreWords, cached.coreWords) {
			t.Errorf("%q: la caché no coincide con el cálculo", c.Name)
		}
		if rowOf(c.Name) != cached {
			t.Errorf("%q: la caché no es estable", c.Name)
		}
	}
	// Varias lecturas a la vez sobre el mismo catálogo: el mismo resultado en todas (y sin carreras, con -race).
	reads := []string{"TAC CRANEO SIMPLE", "RX MIEMBRO INFERIOR AP Y LATERAL", "EMG MMII POR DOLOR", "CONSULTA DX CEFALEA CONTROL NEUROLOGIA"}
	want := make([]CupsNameResolution, len(reads))
	for i, read := range reads {
		want[i] = ResolveCupsByName(read, catalog, false, nil)
	}
	var wg sync.WaitGroup
	for n := 0; n < 4; n++ {
		for i, read := range reads {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if got := ResolveCupsByName(read, catalog, false, nil); got.Code != want[i].Code || len(got.Candidates) != len(want[i].Candidates) {
					t.Errorf("%q: en paralelo da %q/%d, en serie %q/%d", read, got.Code, len(got.Candidates), want[i].Code, len(want[i].Candidates))
				}
			}()
		}
	}
	wg.Wait()
}
