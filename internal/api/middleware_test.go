package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
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
	handler := RateLimiter(5, time.Minute, nil)(inner)

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
	handler := RateLimiter(3, time.Minute, nil)(inner)

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
	handler := RateLimiter(3, time.Minute, nil)(inner)

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

// rateLimitHits manda una petición por cada XFF de xffs desde remoteAddr y cuenta cuántas
// respondió el limitador con 429.
func rateLimitHits(t *testing.T, limit int, proxies *TrustedProxies, remoteAddr string, xffs []string) int {
	t.Helper()
	handler := RateLimiter(limit, time.Minute, proxies)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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

var proxyAddr = netip.MustParseAddr("172.18.0.4")

// failingLookup simula un nombre que no resuelve.
func failingLookup(context.Context, string) ([]netip.Addr, error) {
	return nil, errors.New("no such host")
}

// ngrokProxies simula el DNS de Docker: el servicio ngrok resuelve a 172.18.0.4.
func ngrokProxies() *TrustedProxies {
	return newTrustedProxies([]string{"ngrok"}, func(_ context.Context, host string) ([]netip.Addr, error) {
		if host != "ngrok" {
			return nil, errors.New("no such host")
		}
		return []netip.Addr{proxyAddr}, nil
	}, time.Minute)
}

// xffSeries arma n cabeceras X-Forwarded-For con format, que recibe el índice.
func xffSeries(n int, format string) []string {
	xffs := make([]string, n)
	for i := range xffs {
		xffs[i] = fmt.Sprintf(format, i+1)
	}
	return xffs
}

// TestRateLimiter_SpoofedXFFBehindProxyDoesNotEvade: detrás del túnel, el cliente puede inventar
// las primeras entradas de X-Forwarded-For, pero ngrok AÑADE la IP real al final. Rotar la parte
// inventada no debe abrir un bucket nuevo en cada petición (A-01). El proxy se reconoce por el nombre
// del servicio y por loopback.
func TestRateLimiter_SpoofedXFFBehindProxyDoesNotEvade(t *testing.T) {
	for _, proxy := range []string{"172.18.0.4:51234", "[::ffff:172.18.0.4]:51234", "127.0.0.1:51234", "[::1]:51234"} {
		xffs := xffSeries(10, "10.9.0.%d, 198.51.100.9")
		if got := rateLimitHits(t, 3, ngrokProxies(), proxy, xffs); got != 7 {
			t.Errorf("proxy %s: 10 peticiones del mismo cliente real con XFF inventado, límite 3: esperaba 7 respuestas 429, got %d", proxy, got)
		}
	}
}

// TestRateLimiter_ProxiedClientsHaveSeparateBuckets: detrás del túnel, clientes reales distintos
// (última entrada distinta) no comparten bucket: el límite no se vuelve global.
func TestRateLimiter_ProxiedClientsHaveSeparateBuckets(t *testing.T) {
	if got := rateLimitHits(t, 3, ngrokProxies(), "172.18.0.4:51234", xffSeries(10, "198.51.100.%d")); got != 0 {
		t.Errorf("10 clientes reales distintos tras el proxy: esperaba 0 respuestas 429, got %d", got)
	}
}

// TestRateLimiter_UntrustedSourcesUseRemoteAddr: solo el proxy puede elegir la IP del cliente. Una
// conexión directa, otro contenedor de la misma red, un proxy que no resuelve, una lista vacía (ni
// loopback cuenta) o una última entrada que no es IP se identifican por RemoteAddr.
func TestRateLimiter_UntrustedSourcesUseRemoteAddr(t *testing.T) {
	failing := newTrustedProxies([]string{"ngrok"}, failingLookup, time.Minute)
	cases := []struct {
		name       string
		proxies    *TrustedProxies
		remoteAddr string
		xffs       []string
	}{
		{"directa", ngrokProxies(), "203.0.113.7:40001", xffSeries(10, "198.51.100.%d")},
		{"misma red, no es el proxy", ngrokProxies(), "172.18.0.9:40001", xffSeries(10, "198.51.100.%d")},
		{"el proxy no resuelve", failing, "172.18.0.4:51234", xffSeries(10, "198.51.100.%d")},
		{"sin proxies de confianza", nil, "127.0.0.1:51234", xffSeries(10, "198.51.100.%d")},
		{"lista vacía", NewTrustedProxies(nil), "127.0.0.1:51234", xffSeries(10, "198.51.100.%d")},
		{"última entrada no es IP", ngrokProxies(), "172.18.0.4:51234", xffSeries(10, "198.51.100.9, cliente-%d")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := rateLimitHits(t, 3, tc.proxies, tc.remoteAddr, tc.xffs); got != 7 {
				t.Errorf("10 peticiones desde %s con XFF rotado, límite 3: esperaba 7 respuestas 429, got %d", tc.remoteAddr, got)
			}
		})
	}
}

// TestClientIP_ProxyEntry: la entrada que añade el proxy se acepta con puerto y con espacios, se
// normaliza, y cuenta la última línea si la cabecera viene repetida.
func TestClientIP_ProxyEntry(t *testing.T) {
	proxies := ngrokProxies()
	cases := []struct {
		xff  []string
		want string
	}{
		{[]string{"1.2.3.4, 198.51.100.9 "}, "198.51.100.9"},
		{[]string{"198.51.100.9:4711"}, "198.51.100.9"},
		{[]string{"[2001:db8::1]:4711"}, "2001:db8::1"},
		{[]string{"evil", "198.51.100.9"}, "198.51.100.9"},
		{[]string{"198.51.100.9,"}, "172.18.0.4"},
		{nil, "172.18.0.4"},
	}
	for _, tc := range cases {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = "172.18.0.4:51234"
		for _, v := range tc.xff {
			req.Header.Add("X-Forwarded-For", v)
		}
		if got := clientIP(req, proxies); got != tc.want {
			t.Errorf("XFF %q: got %q, want %q", tc.xff, got, tc.want)
		}
	}
}

// TestTrustedProxies_CachesResolution: el nombre se resuelve una vez por TTL, no en cada petición; y
// un fallo se avisa una sola vez mientras dure.
func TestTrustedProxies_CachesResolution(t *testing.T) {
	lookups := 0
	proxies := newTrustedProxies([]string{"ngrok"}, func(context.Context, string) ([]netip.Addr, error) {
		lookups++
		return []netip.Addr{proxyAddr}, nil
	}, time.Minute)
	rateLimitHits(t, 100, proxies, "172.18.0.4:51234", xffSeries(10, "198.51.100.%d"))
	if lookups != 1 {
		t.Errorf("10 peticiones dentro del TTL: esperaba 1 resolución, got %d", lookups)
	}

	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	failing := newTrustedProxies([]string{"ngrok"}, failingLookup, 0)
	rateLimitHits(t, 100, failing, "172.18.0.4:51234", xffSeries(5, "198.51.100.%d"))
	if n := strings.Count(logs.String(), "level=WARN"); n != 1 {
		t.Errorf("5 resoluciones fallidas seguidas: esperaba 1 WARN, got %d\n%s", n, logs.String())
	}
}

// TestTrustedProxies_TransientFailureKeepsAddresses: si el nombre deja de resolver un momento, el proxy
// se sigue reconociendo por las IPs anteriores (el túnel no pasa a un solo bucket) y se reintenta pronto,
// no al cabo de un TTL entero.
func TestTrustedProxies_TransientFailureKeepsAddresses(t *testing.T) {
	fail := false
	proxies := newTrustedProxies([]string{"ngrok"}, func(context.Context, string) ([]netip.Addr, error) {
		if fail {
			return nil, errors.New("temporary failure in name resolution")
		}
		return []netip.Addr{proxyAddr}, nil
	}, 0)
	if !proxies.trusts(proxyAddr) {
		t.Fatal("con el nombre resuelto, el proxy debe reconocerse")
	}
	fail = true
	proxies.ttl = time.Hour
	if !proxies.trusts(proxyAddr) {
		t.Error("tras un fallo transitorio, el proxy debe seguir reconociéndose por sus IPs anteriores")
	}
	if wait := time.Until(proxies.expiresAt); wait > 2*trustedProxyRetry {
		t.Errorf("tras un fallo se reintenta en %s, want ~%s", wait, trustedProxyRetry)
	}
}

// TestTrustedProxies_SlowLookupDoesNotBlock: mientras una petición resuelve el nombre, las demás no
// esperan: usan la caché vencida si la hay, o RemoteAddr si nunca hubo.
func TestTrustedProxies_SlowLookupDoesNotBlock(t *testing.T) {
	newBlocking := func(primed bool) (*TrustedProxies, chan struct{}, chan struct{}) {
		started, release := make(chan struct{}), make(chan struct{})
		calls := 0
		var once sync.Once
		proxies := newTrustedProxies([]string{"ngrok"}, func(context.Context, string) ([]netip.Addr, error) {
			calls++
			if !primed || calls > 1 {
				once.Do(func() { close(started) })
				<-release
			}
			return []netip.Addr{proxyAddr}, nil
		}, 0)
		if primed {
			proxies.trusts(proxyAddr) // primera resolución, rápida; con TTL 0 queda vencida
		}
		return proxies, started, release
	}
	for _, tc := range []struct {
		name   string
		primed bool
		want   bool
	}{
		{"con caché vencida", true, true},
		{"sin caché", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proxies, started, release := newBlocking(tc.primed)
			done := make(chan struct{})
			go func() { proxies.trusts(proxyAddr); close(done) }()
			<-started

			got := make(chan bool, 1)
			go func() { got <- proxies.trusts(proxyAddr) }()
			select {
			case v := <-got:
				if v != tc.want {
					t.Errorf("con una resolución en vuelo: trusts = %v, want %v", v, tc.want)
				}
			case <-time.After(time.Second):
				t.Error("una petición esperó a la resolución en vuelo de otra")
			}
			close(release)
			<-done
		})
	}
}

// TestRateLimiter_IPv6GroupedBy64: un cliente IPv6 suele tener un /64 entero; rotar la parte baja de
// la dirección no le da cupos nuevos. Otro /64 sí es otro cliente, y las IPv4 siguen por dirección.
func TestRateLimiter_IPv6GroupedBy64(t *testing.T) {
	handler := RateLimiter(3, time.Minute, ngrokProxies())(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	hits := func(remoteAddr, xff string) int {
		req := httptest.NewRequest("GET", "/api/internal/sessions", nil)
		req.RemoteAddr = remoteAddr
		if xff != "" {
			req.Header.Set("X-Forwarded-For", xff)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code == http.StatusTooManyRequests {
			return 1
		}
		return 0
	}
	got := 0
	for i := 1; i <= 5; i++ {
		got += hits(fmt.Sprintf("[2001:db8:1:2::%x]:40001", i), "")
		got += hits("172.18.0.4:51234", fmt.Sprintf("[2001:db8:1:2:aaaa::%x]:4711", i))
	}
	if got != 7 {
		t.Errorf("10 peticiones desde un mismo /64 (directas y por el túnel), límite 3: esperaba 7 respuestas 429, got %d", got)
	}
	if hits("[2001:db8:1:3::1]:40001", "") != 0 {
		t.Error("otro /64 comparte el cupo")
	}
	if hits("198.51.100.1:40001", "")+hits("198.51.100.2:40001", "") != 0 {
		t.Error("dos IPv4 distintas comparten el cupo")
	}
}
