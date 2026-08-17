package bird

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// H149: un contacto que NO existe (404 al resolver) sí es un fallo permanente — no hay vía de
// entrega y reintentarlo mañana es trabajo inútil.
func TestSendText_BsuidResolve404_IsPermanent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"code":"NotFound"}`))
	}))
	defer srv.Close()

	_, err := NewClientForTest(srv.URL).SendText("laura.perez", "", "Hola")
	if !errors.Is(err, ErrNonContactable) {
		t.Fatalf("404 debe ser permanente (ErrNonContactable), got %v", err)
	}
	if !IsPermanentSendError(err) {
		t.Error("404 al resolver debe clasificar como permanente")
	}
}

// H149: el contacto existe pero no tiene identifier whatsapp_* → tampoco hay vía de entrega.
// Permanente.
func TestSendText_BsuidMissingIdentifier_IsPermanent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"id":"c-1","featuredIdentifiers":[{"key":"emailaddress","value":"a@b.c"}]}`))
	}))
	defer srv.Close()

	_, err := NewClientForTest(srv.URL).SendText("laura.perez", "", "Hola")
	if !errors.Is(err, ErrNonContactable) {
		t.Fatalf("contacto sin BSUID debe ser permanente, got %v", err)
	}
}

// EL CASO QUE MOTIVA EL FIX (H149): Bird responde 500 al resolver el BSUID. Antes esto se
// aplastaba a ErrNonContactable (permanente) y la lista de espera marcaba al paciente
// 'unreachable' — un estado del que NINGUNA consulta del pool lo rescata: quedaba fuera para
// siempre por un hipo de 30 segundos. Debe ser TRANSITORIO (la entrada vuelve a 'waiting').
func TestSendText_BsuidResolve500_IsTransient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte(`{"code":"InternalError"}`))
	}))
	defer srv.Close()

	_, err := NewClientForTest(srv.URL).SendText("laura.perez", "", "Hola")
	if err == nil {
		t.Fatal("esperaba error con Bird caído")
	}
	if errors.Is(err, ErrNonContactable) {
		t.Error("un 500 de Bird NO debe marcar al contacto como no-contactable (lo saca del pool para siempre)")
	}
	if IsPermanentSendError(err) {
		t.Error("un 500 debe clasificar como transitorio (reintentable mañana, sin costo de envío)")
	}
}

// Un 429 al resolver tampoco condena al contacto.
func TestSendTemplate_BsuidResolve429_IsTransient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(429)
	}))
	defer srv.Close()

	_, err := NewClientForTest(srv.URL).SendTemplate("laura.perez", TemplateConfig{ProjectID: "p"})
	if err == nil {
		t.Fatal("esperaba error")
	}
	if IsPermanentSendError(err) {
		t.Error("429 al resolver el BSUID debe ser transitorio, no permanente")
	}
}

// El fallo transitorio no debe costar envíos: la resolución es un PATCH de contacto, no un
// mensaje. Se verifica que no se postee nada al endpoint de mensajes/templates.
func TestBsuidTransientFailure_NoChargeableSend(t *testing.T) {
	var chargeable atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			chargeable.Add(1)
		}
		w.WriteHeader(503)
	}))
	defer srv.Close()

	_, _ = NewClientForTest(srv.URL).SendText("laura.perez", "", "Hola")
	if chargeable.Load() != 0 {
		t.Errorf("un fallo de resolución no debe postear ningún mensaje cobrable, hubo %d", chargeable.Load())
	}
}
