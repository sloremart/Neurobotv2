package api

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRequestLogger_NoPanic(t *testing.T) {
	handler := RequestLogger(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))

	req := httptest.NewRequest("GET", "/health", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestInternalAuth_CorrectKey(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte("ok"))
	})
	handler := InternalAuth("secret-key")(inner)

	req := httptest.NewRequest("GET", "/internal/kpi", nil)
	req.Header.Set("X-API-Key", "secret-key")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestInternalAuth_WrongKey(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not reach handler with wrong key")
	})
	handler := InternalAuth("secret-key")(inner)

	req := httptest.NewRequest("GET", "/internal/kpi", nil)
	req.Header.Set("X-API-Key", "wrong-key")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestInternalAuth_MissingKey(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not reach handler without key")
	})
	handler := InternalAuth("secret-key")(inner)

	req := httptest.NewRequest("GET", "/internal/kpi", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestInternalAuth_EmptyKeyRejects(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not reach handler when API key is empty")
	})
	handler := InternalAuth("")(inner)

	req := httptest.NewRequest("GET", "/internal/kpi", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 when API key is empty, got %d", rec.Code)
	}
}

func TestMaxBodySize_RejectsOversized(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
			return
		}
		w.WriteHeader(200)
	})
	handler := MaxBodySize(inner)

	// 2 MB body — exceeds 1 MB limit
	bigBody := bytes.NewReader(make([]byte, 2<<20))
	req := httptest.NewRequest("POST", "/api/internal/cancel-agenda", bigBody)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("expected 413 for oversized body, got %d", rec.Code)
	}
}

func TestMaxBodySize_AllowsNormalBody(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read error", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(200)
	})
	handler := MaxBodySize(inner)

	body := strings.NewReader(`{"agenda_id":1,"date":"2026-03-20"}`)
	req := httptest.NewRequest("POST", "/api/internal/cancel-agenda", body)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Errorf("expected 200 for normal body, got %d", rec.Code)
	}
}

func TestRateLimiter_AllowsUnderLimit(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	})
	handler := RateLimiter(5, time.Minute)(inner)

	for i := 0; i < 5; i++ {
		req := httptest.NewRequest("GET", "/api/internal/kpis/health", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Errorf("request %d: expected 200, got %d", i+1, rec.Code)
		}
	}
}

// TestRateLimiter_SameHostDifferentPorts verifica el fix N-14: el límite se keyea por HOST,
// no por "host:port". Varias conexiones del mismo host con puerto efímero distinto deben caer
// en el mismo bucket (antes del fix cada puerto era un bucket nuevo → el límite no aplicaba).
func TestRateLimiter_SameHostDifferentPorts(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	})
	handler := RateLimiter(3, time.Minute)(inner)

	ports := []string{"40001", "40002", "40003", "40004"}
	var lastCode int
	for _, p := range ports {
		req := httptest.NewRequest("GET", "/test", nil)
		req.RemoteAddr = "203.0.113.7:" + p // mismo host, puerto distinto
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		lastCode = rec.Code
	}

	if lastCode != http.StatusTooManyRequests {
		t.Errorf("4ta request del mismo host (distinto puerto) debió ser 429, got %d", lastCode)
	}
}

func TestRateLimiter_BlocksOverLimit(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	})
	handler := RateLimiter(3, time.Minute)(inner)

	// 3 allowed
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest("GET", "/test", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
	}

	// 4th should be blocked
	req := httptest.NewRequest("GET", "/test", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("expected 429 over rate limit, got %d", rec.Code)
	}
}

// rateLimitHits manda len(xffs) peticiones desde remoteAddr, cada una con su X-Forwarded-For,
// y cuenta cuántas respondió el limitador con 429.
func rateLimitHits(t *testing.T, limit int, remoteAddr string, xffs []string) int {
	t.Helper()
	handler := RateLimiter(limit, time.Minute)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	got429 := 0
	for _, xff := range xffs {
		req := httptest.NewRequest("GET", "/api/internal/kpis/health", nil)
		req.RemoteAddr = remoteAddr
		if xff != "" {
			req.Header.Set("X-Forwarded-For", xff)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code == http.StatusTooManyRequests {
			got429++
		}
	}
	return got429
}

// TestRateLimiter_SpoofedXFFBehindProxyDoesNotEvade: detrás del túnel, el cliente puede inventar
// las primeras entradas de X-Forwarded-For, pero ngrok AÑADE la IP real al final. Rotar la parte
// inventada no debe abrir un bucket nuevo en cada petición (A-01).
func TestRateLimiter_SpoofedXFFBehindProxyDoesNotEvade(t *testing.T) {
	for _, proxy := range []string{"172.18.0.4:51234", "127.0.0.1:51234", "[::1]:51234"} {
		xffs := make([]string, 10)
		for i := range xffs {
			xffs[i] = fmt.Sprintf("10.9.%d.%d, 198.51.100.9", i, i+1)
		}
		if got := rateLimitHits(t, 3, proxy, xffs); got != 7 {
			t.Errorf("proxy %s: 10 peticiones del mismo cliente real con XFF inventado, límite 3: esperaba 7 respuestas 429, got %d", proxy, got)
		}
	}
}

// TestRateLimiter_ProxiedClientsHaveSeparateBuckets: detrás del túnel, clientes reales distintos
// (última entrada distinta) no comparten bucket: el límite no se vuelve global.
func TestRateLimiter_ProxiedClientsHaveSeparateBuckets(t *testing.T) {
	xffs := make([]string, 10)
	for i := range xffs {
		xffs[i] = fmt.Sprintf("198.51.100.%d", i+1)
	}
	if got := rateLimitHits(t, 3, "172.18.0.4:51234", xffs); got != 0 {
		t.Errorf("10 clientes reales distintos tras el proxy: esperaba 0 respuestas 429, got %d", got)
	}
}

// TestRateLimiter_DirectRequestIgnoresXFF: una conexión que no viene del proxy (puerto publicado)
// se identifica por RemoteAddr; su X-Forwarded-For, venga como venga, no cuenta.
func TestRateLimiter_DirectRequestIgnoresXFF(t *testing.T) {
	xffs := make([]string, 10)
	for i := range xffs {
		xffs[i] = fmt.Sprintf("198.51.100.%d", i+1)
	}
	if got := rateLimitHits(t, 3, "203.0.113.7:40001", xffs); got != 7 {
		t.Errorf("10 peticiones directas desde la misma IP con XFF rotado, límite 3: esperaba 7 respuestas 429, got %d", got)
	}
}
