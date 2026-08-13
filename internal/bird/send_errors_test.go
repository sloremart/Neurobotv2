package bird

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// Un 4xx de la API de canales debe llegar al caller como APIError tipado (con el status),
// clasificado como permanente: reintentarlo es un envío cobrado que jamás entregará.
func TestSendText_Channels422_IsPermanentAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(422)
		_, _ = w.Write([]byte(`{"code":"InvalidPayload"}`))
	}))
	defer srv.Close()

	c := NewClientForTest(srv.URL)
	_, err := c.SendText("+573001234567", "", "Hola")
	if err == nil {
		t.Fatal("esperaba error en 422")
	}
	var ae *APIError
	if !errors.As(err, &ae) {
		t.Fatalf("esperaba *APIError, got %T: %v", err, err)
	}
	if ae.Status != 422 {
		t.Errorf("esperaba status 422, got %d", ae.Status)
	}
	if !IsPermanentSendError(err) {
		t.Error("un 422 debe clasificar como permanente (no reintentar)")
	}
}

func TestIsPermanentSendError_Classification(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"422 permanente", &APIError{Status: 422}, true},
		{"400 permanente", &APIError{Status: 400}, true},
		{"429 transitorio", &APIError{Status: 429}, false},
		{"408 transitorio", &APIError{Status: 408}, false},
		{"APIError envuelto", fmt.Errorf("send: %w", &APIError{Status: 404}), true},
		{"error de red plano", errors.New("dial tcp: connection refused"), false},
		{"5xx tras reintentos", errors.New("bird api 5xx after 3 attempts: status 500"), false},
		{"no contactable", fmt.Errorf("x: %w", ErrNonContactable), true},
	}
	for _, tc := range cases {
		if got := IsPermanentSendError(tc.err); got != tc.want {
			t.Errorf("%s: IsPermanentSendError=%v, want %v", tc.name, got, tc.want)
		}
	}
}

// Un identificador que no es E.164 (nombres como "felipe.rubio") NUNCA se envía por la Channels
// API con el valor crudo. Desde el fix BSUID (1a0c89e) SÍ se intenta resolver el contacto (PATCH)
// y su conversación; si NINGUNA vía existe, termina en ErrNonContactable y la Channels API de
// mensajes jamás recibe el identificador crudo.
func TestSendText_NonE164_NoHTTPCall(t *testing.T) {
	var channelHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/channels/") {
			channelHits.Add(1)
		}
		// Resolución BSUID y creación de conversación fallan (contacto irresoluble).
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"code":"NotFound"}`))
	}))
	defer srv.Close()

	c := NewClientForTest(srv.URL)
	_, err := c.SendText("felipe.rubio", "", "Hola")
	if !errors.Is(err, ErrNonContactable) {
		t.Fatalf("esperaba ErrNonContactable, got %v", err)
	}
	if channelHits.Load() != 0 {
		t.Errorf("la Channels API no debe recibir el identificador crudo, hubo %d llamadas", channelHits.Load())
	}
}

func TestSendTemplate_NonE164_NoHTTPCall(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"id":"x"}`))
	}))
	defer srv.Close()

	c := NewClientForTest(srv.URL)
	_, err := c.SendTemplate("maria.gomez", TemplateConfig{ProjectID: "p"})
	if !errors.Is(err, ErrNonContactable) {
		t.Fatalf("esperaba ErrNonContactable, got %v", err)
	}
	if hits.Load() != 0 {
		t.Errorf("no debe haber llamada HTTP para identificador no-E.164, hubo %d", hits.Load())
	}
}

// La lista interactiva con identificador no-E.164 irresoluble (sin BSUID ni conversación)
// termina en ErrNonContactable; la Channels API de mensajes jamás recibe el valor crudo.
func TestSendList_NonE164Fallback_NoHTTPCall(t *testing.T) {
	var channelHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/channels/") {
			channelHits.Add(1)
		}
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"code":"NotFound"}`))
	}))
	defer srv.Close()

	c := NewClientForTest(srv.URL)
	sections := []ListSection{{Title: "S", Rows: []ListRow{{ID: "r1", Title: "Row"}}}}
	_, err := c.SendList("yerly.castro", "", "body", "Ver", sections)
	if !errors.Is(err, ErrNonContactable) {
		t.Fatalf("esperaba ErrNonContactable, got %v", err)
	}
	if channelHits.Load() != 0 {
		t.Errorf("la Channels API no debe recibir el identificador crudo, hubo %d llamadas", channelHits.Load())
	}
}
