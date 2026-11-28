package results

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func serve(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return NewClient(srv.URL, "")
}

func TestSearch_List(t *testing.T) {
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/buscar" || r.URL.Query().Get("cedula") != "123" {
			t.Errorf("request inesperado: %s", r.URL)
		}
		_, _ = w.Write([]byte(`[{"nombre":"ANA PEREZ","fecha":"20/11/2026 08:30","admision":"A1","tipo_estudio":"RM","archivo":"a.pdf"},
			{"nombre":"ANA PEREZ","fecha":"x","admision":"A2","tipo_estudio":"TAC","archivo":"b.pdf"}]`))
	})
	got, err := c.Search(context.Background(), "123")
	if err != nil || len(got) != 2 {
		t.Fatalf("got %v, err %v", got, err)
	}
	s := got[0]
	want := time.Date(2026, 11, 20, 8, 30, 0, 0, time.Local)
	if s.PatientName != "ANA PEREZ" || s.Admission != "A1" || s.StudyType != "RM" || s.File != "a.pdf" ||
		!s.Date.Equal(want) || s.RawDate != "20/11/2026 08:30" {
		t.Errorf("mapeo incorrecto: %+v", s)
	}
	if !got[1].Date.IsZero() || got[1].RawDate != "x" {
		t.Errorf("fecha ilegible debe quedar en cero: %+v", got[1])
	}
}

func TestSearch_SingleObject(t *testing.T) {
	c := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"nombre":"ANA","fecha":"20/11/2026 08:30","admision":"A1","tipo_estudio":"RM","archivo":"a.pdf"}`))
	})
	got, err := c.Search(context.Background(), "1")
	if err != nil || len(got) != 1 || got[0].File != "a.pdf" {
		t.Fatalf("got %v, err %v", got, err)
	}
}

func TestSearch_EmptyAndErrors(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		wantErr bool
	}{
		{"empty list", 200, `[]`, false},
		{"404", 404, ``, false},
		{"400 invalid", 400, `{"error":"Cédula inválida."}`, false},
		{"500", 500, `boom`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := serve(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			got, err := c.Search(context.Background(), "1")
			if (err != nil) != tc.wantErr || len(got) != 0 {
				t.Errorf("got %v, err %v", got, err)
			}
		})
	}
}

func TestSearch_SendsAPIKeyHeaderWhenSet(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("X-API-Key")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	if _, err := NewClient(srv.URL, "k1").Search(context.Background(), "1"); err != nil || got != "k1" {
		t.Errorf("header = %q, err %v", got, err)
	}
	if _, err := NewClient(srv.URL, "").Search(context.Background(), "1"); err != nil || got != "" {
		t.Errorf("sin clave no debe enviar cabecera, got %q", got)
	}
}

func TestSearch_Timeout10s(t *testing.T) {
	c := serve(t, func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	if c.searchTimeout != 10*time.Second {
		t.Fatalf("plazo = %v", c.searchTimeout)
	}
	c.searchTimeout = 50 * time.Millisecond
	_, err := c.Search(context.Background(), "1")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v", err)
	}
}

func TestDownloadPDF_OK(t *testing.T) {
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != "/api/pdf/a%20b.pdf" {
			t.Errorf("ruta = %s", r.URL.EscapedPath())
		}
		_, _ = w.Write([]byte("%PDF-1"))
	})
	data, err := c.DownloadPDF(context.Background(), "a b.pdf")
	if err != nil || string(data) != "%PDF-1" {
		t.Errorf("data %q, err %v", data, err)
	}
}

func TestDownloadPDF_RejectsInvalidFile(t *testing.T) {
	called := false
	c := serve(t, func(_ http.ResponseWriter, _ *http.Request) { called = true })
	for _, f := range []string{"../x.pdf", "a/b.pdf", "a\\b.pdf", "", ".."} {
		if _, err := c.DownloadPDF(context.Background(), f); !errors.Is(err, ErrInvalidFile) {
			t.Errorf("%q: err = %v", f, err)
		}
	}
	if called {
		t.Error("no debe llamar al servidor")
	}
}

func TestDownloadPDF_TooLarge(t *testing.T) {
	c := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("a", MaxPDFBytes+1)))
	})
	if _, err := c.DownloadPDF(context.Background(), "a.pdf"); !errors.Is(err, ErrTooLarge) {
		t.Errorf("err = %v", err)
	}
}

func TestDownloadPDF_Timeout20s(t *testing.T) {
	c := serve(t, func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	if c.downloadTimeout != 20*time.Second {
		t.Fatalf("plazo = %v", c.downloadTimeout)
	}
	c.downloadTimeout = 50 * time.Millisecond
	_, err := c.DownloadPDF(context.Background(), "a.pdf")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v", err)
	}
}

// Un 3xx no se sigue: la clave no viaja a otro anfitrión y no se sirve nada de una URL sin validar.
func TestClient_DoesNotFollowRedirects(t *testing.T) {
	var hits int
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.Header.Get("X-API-Key") != "" {
			t.Error("la clave llegó al destino de la redirección")
		}
		_, _ = w.Write([]byte("%PDF-1.4 ajeno"))
	}))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/x", http.StatusFound)
	}))
	defer origin.Close()
	c := NewClient(origin.URL, "secreta")

	if _, err := c.DownloadPDF(context.Background(), "a.pdf"); err == nil {
		t.Error("DownloadPDF: un 302 debe ser error")
	}
	if got, err := c.Search(context.Background(), "123"); err == nil || got != nil {
		t.Errorf("Search: un 302 debe ser error sin datos, fue %v, %v", got, err)
	}
	if hits != 0 {
		t.Errorf("el destino recibió %d peticiones", hits)
	}
}
