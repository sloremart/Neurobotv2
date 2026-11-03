package nectar

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// Néctar no redirige ninguna ruta del bot. Un 3xx (un proxy mal puesto, un host comprometido) no
// se sigue: seguirlo reenviaría la clave de servicio y la firma a otro anfitrión, porque Go solo
// quita Authorization y Cookie al cambiar de dominio, no las cabeceras propias.
func TestClient_DoesNotFollowRedirects(t *testing.T) {
	var reached atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		reached.Add(1)
		if r.Header.Get(ServiceKeyHeader) != "" || r.Header.Get(SignatureHeader) != "" {
			t.Errorf("las credenciales de Néctar llegaron a otro anfitrión")
		}
	}))
	defer other.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()

	c := NewClient(origin.URL, svcKey, reqSecret)
	if _, err := c.GetState(context.Background(), 1234); err == nil {
		t.Fatal("un 3xx no es una respuesta válida")
	}
	if _, err := c.SendText(context.Background(), 1234, "hola"); err == nil {
		t.Fatal("un 3xx no es una respuesta válida")
	}
	if reached.Load() != 0 {
		t.Fatalf("la redirección se siguió %d veces", reached.Load())
	}
}
