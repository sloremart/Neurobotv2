package bird

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
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

// Un identificador que no es E.164 (nombres como "felipe.rubio") NUNCA puede entregarse por la
// Channels API: el gate debe cortar ANTES de la llamada HTTP (cero costo) con ErrNonContactable.
func TestSendText_NonE164_NoHTTPCall(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"id":"x"}`))
	}))
	defer srv.Close()

	c := NewClientForTest(srv.URL)
	_, err := c.SendText("felipe.rubio", "", "Hola")
	if !errors.Is(err, ErrNonContactable) {
		t.Fatalf("esperaba ErrNonContactable, got %v", err)
	}
	if hits.Load() != 0 {
		t.Errorf("no debe haber llamada HTTP para identificador no-E.164, hubo %d", hits.Load())
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

// La lista interactiva sin conversación cae al fallback de texto por Channels: con identificador
// no-E.164 el fallback también debe cortar sin llamada HTTP.
func TestSendList_NonE164Fallback_NoHTTPCall(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"id":"x"}`))
	}))
	defer srv.Close()

	c := NewClientForTest(srv.URL)
	sections := []ListSection{{Title: "S", Rows: []ListRow{{ID: "r1", Title: "Row"}}}}
	_, err := c.SendList("yerly.castro", "", "body", "Ver", sections)
	if !errors.Is(err, ErrNonContactable) {
		t.Fatalf("esperaba ErrNonContactable, got %v", err)
	}
	if hits.Load() != 0 {
		t.Errorf("no debe haber llamada HTTP para identificador no-E.164, hubo %d", hits.Load())
	}
}
