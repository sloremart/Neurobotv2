package api

import (
	"crypto/subtle"
	"log/slog"
	"net"
	"net/http"
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
// maxRequests por ventana de tiempo.
func RateLimiter(maxRequests int, window time.Duration) func(http.Handler) http.Handler {
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
			// Keyear por HOST, no por "host:port": el puerto efímero cambia en cada conexión TCP,
			// lo que vaciaba el bucket y hacía inefectivo el límite (N-14). Fallback a RemoteAddr
			// crudo si no trae puerto.
			ip := r.RemoteAddr
			if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
				ip = host
			}

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

// SecurityHeaders adds standard security headers to all responses.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}
