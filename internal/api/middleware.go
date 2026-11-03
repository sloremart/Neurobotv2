package api

import (
	"crypto/subtle"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

// maxRequestBodySize limits the size of incoming request bodies (1 MB).
const maxRequestBodySize = 1 << 20

// RequestLogger middleware para logging de requests (skips /health to reduce noise)
func RequestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		next.ServeHTTP(w, r)
		slog.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"duration_ms", time.Since(start).Milliseconds(),
		)
	})
}

// InternalAuth middleware para endpoints internos (requiere X-API-Key header).
// Si apiKey está vacío, rechaza TODOS los requests (fail-closed).
func InternalAuth(apiKey string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if apiKey == "" {
				slog.Warn("internal API key not configured, rejecting request", "path", r.URL.Path)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			key := r.Header.Get("X-API-Key")
			if subtle.ConstantTimeCompare([]byte(key), []byte(apiKey)) != 1 {
				slog.Warn("security: invalid API key",
					"path", r.URL.Path,
					"ip", r.RemoteAddr,
					"has_key", key != "",
				)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// MaxBodySize middleware que limita el tamaño del body de requests POST/PUT/PATCH.
func MaxBodySize(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil && r.ContentLength != 0 {
			r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodySize)
		}
		next.ServeHTTP(w, r)
	})
}

// RateLimiter middleware que limita requests por IP con token bucket simple.
// maxRequests por ventana de tiempo. proxies decide de quién se cree el X-Forwarded-For (ver clientIP).
func RateLimiter(maxRequests int, window time.Duration, proxies *TrustedProxies) func(http.Handler) http.Handler {
	type client struct {
		count   int
		resetAt time.Time
	}

	var mu sync.Mutex
	clients := make(map[string]*client)

	// Cleanup goroutine
	go func() {
		for {
			time.Sleep(window)
			mu.Lock()
			now := time.Now()
			for ip, c := range clients {
				if now.After(c.resetAt) {
					delete(clients, ip)
				}
			}
			mu.Unlock()
		}
	}()

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := clientIP(r, proxies)

			mu.Lock()
			c, exists := clients[ip]
			now := time.Now()
			if !exists || now.After(c.resetAt) {
				clients[ip] = &client{count: 1, resetAt: now.Add(window)}
				mu.Unlock()
				next.ServeHTTP(w, r)
				return
			}
			c.count++
			if c.count > maxRequests {
				mu.Unlock()
				slog.Warn("rate limit exceeded", "ip", ip, "path", r.URL.Path)
				http.Error(w, "too many requests", http.StatusTooManyRequests)
				return
			}
			mu.Unlock()
			next.ServeHTTP(w, r)
		})
	}
}

// trustedProxyTTL es cada cuánto se vuelve a resolver el nombre del proxy: su IP cambia cuando
// Docker recrea el contenedor.
const trustedProxyTTL = time.Minute

// TrustedProxies reconoce al proxy de confianza por su IDENTIDAD (el nombre del servicio en la red de
// Docker), no por subred: la red bridge puede caer en cualquier rango y en ella viven otros
// contenedores. Un nil o una lista vacía no confía en nadie, ni en loopback.
type TrustedProxies struct {
	hosts  []string
	lookup func(host string) ([]net.IP, error)
	ttl    time.Duration

	mu        sync.Mutex
	addrs     map[netip.Addr]struct{}
	expiresAt time.Time
	failing   map[string]bool // hosts que fallaron en la última resolución (para avisar una sola vez)
}

// NewTrustedProxies crea el reconocedor para los nombres de hosts (TRUSTED_PROXY_HOSTS).
func NewTrustedProxies(hosts []string) *TrustedProxies {
	return newTrustedProxies(hosts, net.LookupIP, trustedProxyTTL)
}

func newTrustedProxies(hosts []string, lookup func(string) ([]net.IP, error), ttl time.Duration) *TrustedProxies {
	return &TrustedProxies{hosts: hosts, lookup: lookup, ttl: ttl, failing: map[string]bool{}}
}

// trusts dice si addr es el proxy: loopback o una IP que resuelve alguno de sus nombres. Un nombre que
// no resuelve no aporta IPs (se avisa al empezar a fallar), así que su conexión se identifica por
// RemoteAddr: modo seguro, no evadible.
func (t *TrustedProxies) trusts(addr netip.Addr) bool {
	if t == nil || len(t.hosts) == 0 {
		return false
	}
	if addr.IsLoopback() {
		return true
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if now := time.Now(); t.addrs == nil || !now.Before(t.expiresAt) {
		t.addrs = map[netip.Addr]struct{}{}
		for _, host := range t.hosts {
			ips, err := t.lookup(host)
			if err != nil {
				if !t.failing[host] {
					slog.Warn("trusted proxy not resolvable, rate limiting by RemoteAddr", "host", host, "error", err)
				}
				t.failing[host] = true
				continue
			}
			delete(t.failing, host)
			for _, ip := range ips {
				if a, ok := netip.AddrFromSlice(ip); ok {
					t.addrs[a.Unmap()] = struct{}{}
				}
			}
		}
		t.expiresAt = now.Add(t.ttl)
	}
	_, ok := t.addrs[addr]
	return ok
}

// clientIP identifica al cliente para el limitador. Topología asumida (docker-compose.yml): un solo
// proxy delante del bot, el contenedor ngrok de la red botnet, que AÑADE la IP del cliente al final de
// X-Forwarded-For (no la reemplaza ni manda otra cabecera con la IP real); el resto de la cabecera lo
// escribe el cliente. Si la conexión viene del proxy se toma la ÚLTIMA entrada, siempre que sea una IP;
// si no (puerto publicado, LAN, otros contenedores), manda RemoteAddr. Se keyea por HOST, no por
// "host:port": el puerto efímero cambia en cada conexión TCP (N-14).
func clientIP(r *http.Request, proxies *TrustedProxies) string {
	host := r.RemoteAddr
	if h, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		host = h
	}
	addr, err := netip.ParseAddr(host)
	if err != nil || !proxies.trusts(addr.Unmap()) {
		return host
	}
	xff := r.Header.Values("X-Forwarded-For")
	if len(xff) == 0 {
		return host
	}
	entries := strings.Split(xff[len(xff)-1], ",")
	last := strings.TrimSpace(entries[len(entries)-1])
	if h, _, err := net.SplitHostPort(last); err == nil {
		last = h
	}
	if client, err := netip.ParseAddr(last); err == nil {
		return client.Unmap().String()
	}
	return host
}

// SecurityHeaders adds standard security headers to all responses.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}
