package siesa

import (
	"testing"
	"time"
)

// Auditoría queries M7: la clave del caché es `from|to` del query string → cada rango distinto
// crea una entrada y nada las borraba (fuga de memoria). store() purga las vencidas.
func TestAnalyticsCacheEvictsExpiredOnStore(t *testing.T) {
	r := NewAnalyticsRepo(nil, 10*time.Minute)
	r.cache["vieja"] = cacheEntry{at: time.Now().Add(-time.Hour), data: 1}
	r.cache["vigente"] = cacheEntry{at: time.Now(), data: 2}

	r.store("nueva", 3)

	if _, ok := r.cache["vieja"]; ok {
		t.Error("la entrada vencida debe purgarse al almacenar")
	}
	if _, ok := r.cache["vigente"]; !ok {
		t.Error("la entrada vigente debe conservarse")
	}
	if _, ok := r.cache["nueva"]; !ok {
		t.Error("la entrada nueva debe almacenarse")
	}
}
