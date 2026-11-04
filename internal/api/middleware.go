package api

import (
	"context"
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
// Si apiKey está vacío, rechaza TODOS los requests (fail-closed). Los WARN de rechazo van con tope.
func InternalAuth(apiKey string) func(http.Handler) http.Handler {
	authLog := newAuthLogSampler()
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if apiKey == "" {
				authLog.Warn("internal API key not configured, rejecting request", "path", r.URL.Path)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			key := r.Header.Get("X-API-Key")
			if subtle.ConstantTimeCompare([]byte(key), []byte(apiKey)) != 1 {
				authLog.Warn("security: invalid API key",
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
	exceededLog := newAuthLogSampler()

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
				exceededLog.Warn("rate limit exceeded", "ip", ip, "path", r.URL.Path)
				http.Error(w, "too many requests", http.StatusTooManyRequests)
				return
			}
			mu.Unlock()
			next.ServeHTTP(w, r)
		})
	}
}

// Resolución del nombre del proxy: se renueva cada trustedProxyTTL (su IP cambia cuando Docker recrea
// el contenedor); tras un fallo se reintenta a los trustedProxyRetry; cada consulta DNS tiene
// trustedProxyLookupTimeout.
const (
	trustedProxyTTL           = time.Minute
	trustedProxyRetry         = 5 * time.Second
	trustedProxyLookupTimeout = time.Second
)

// TrustedProxies reconoce al proxy de confianza por su IDENTIDAD (el nombre del servicio en la red de
// Docker), no por subred: la red bridge puede caer en cualquier rango y en ella viven otros
// contenedores. Un nil o una lista vacía no confía en nadie, ni en loopback.
type TrustedProxies struct {
	hosts  []string
	lookup func(ctx context.Context, host string) ([]netip.Addr, error)
	ttl    time.Duration
	retry  time.Duration

	mu         sync.Mutex
	addrs      map[string][]netip.Addr // última resolución buena de cada nombre; nil = nunca se resolvió
	expiresAt  time.Time
	refreshing bool            // hay una resolución en vuelo
	failing    map[string]bool // nombres que fallaron en la última resolución (para avisar una sola vez)
}

// NewTrustedProxies crea el reconocedor para los nombres de hosts (TRUSTED_PROXY_HOSTS). Sin nombres
// avisa: el límite de la API interna contará a todo el túnel como un solo cliente.
func NewTrustedProxies(hosts []string) *TrustedProxies {
	if len(hosts) == 0 {
		slog.Warn("TRUSTED_PROXY_HOSTS is empty: X-Forwarded-For is ignored, internal API rate limit keyed by RemoteAddr")
	}
	return newTrustedProxies(hosts, func(ctx context.Context, host string) ([]netip.Addr, error) {
		return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	}, trustedProxyTTL)
}

func newTrustedProxies(hosts []string, lookup func(context.Context, string) ([]netip.Addr, error), ttl time.Duration) *TrustedProxies {
	return &TrustedProxies{hosts: hosts, lookup: lookup, ttl: ttl, retry: trustedProxyRetry, failing: map[string]bool{}}
}

// trusts dice si addr es el proxy: loopback o una IP que resuelve alguno de sus nombres. Si la caché
// venció, esta petición la renueva FUERA del lock; las que llegan mientras tanto usan la caché vieja sin
// esperar (o RemoteAddr si nunca hubo). Un nombre que deja de resolver conserva sus IPs anteriores y se
// reintenta pronto; si nunca resolvió no aporta IPs y su conexión se identifica por RemoteAddr: modo
// seguro, no evadible.
func (t *TrustedProxies) trusts(addr netip.Addr) bool {
	if t == nil || len(t.hosts) == 0 {
		return false
	}
	if addr.IsLoopback() {
		return true
	}
	t.mu.Lock()
	if !t.refreshing && (t.addrs == nil || !time.Now().Before(t.expiresAt)) {
		t.refreshing = true
		t.mu.Unlock()
		t.refresh()
		t.mu.Lock()
	}
	defer t.mu.Unlock()
	for _, ips := range t.addrs {
		for _, ip := range ips {
			if ip == addr {
				return true
			}
		}
	}
	return false
}

// refresh resuelve todos los nombres sin el lock y publica el resultado.
func (t *TrustedProxies) refresh() {
	resolved := make(map[string][]netip.Addr, len(t.hosts))
	errs := make(map[string]error)
	for _, host := range t.hosts {
		ctx, cancel := context.WithTimeout(context.Background(), trustedProxyLookupTimeout)
		ips, err := t.lookup(ctx, host)
		cancel()
		if err != nil {
			errs[host] = err
			continue
		}
		for i := range ips {
			ips[i] = ips[i].Unmap()
		}
		resolved[host] = ips
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if t.addrs == nil {
		t.addrs = map[string][]netip.Addr{}
	}
	for host, ips := range resolved {
		t.addrs[host] = ips
		delete(t.failing, host)
	}
	for host, err := range errs {
		if !t.failing[host] {
			slog.Warn("trusted proxy not resolvable, keeping previous addresses (if any) and retrying",
				"host", host, "previous", len(t.addrs[host]), "error", err)
		}
		t.failing[host] = true
	}
	next := t.ttl
	if len(errs) > 0 {
		next = min(t.retry, t.ttl)
	}
	t.expiresAt = time.Now().Add(next)
	t.refreshing = false
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
