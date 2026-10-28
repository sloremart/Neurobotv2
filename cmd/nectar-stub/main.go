// Comando nectar-stub: doble artificial de Néctar para la prueba de humo local
// (docs/PRUEBA-LOCAL-NECTAR.md).
//
// El bot habla con Néctar por las siete rutas de internal/nectar/client.go. Este binario
// contesta esas rutas con cuerpos válidos, verifica la clave de servicio y la firma X-Nectar-*
// de cada llamada saliente del bot, y GUARDA lo que recibió (GET /records) para poder afirmar
// qué mandó el bot: el POST /messages de la lista, el sobre de escalado v1, el cierre.
//
// No tiene estado de negocio ni BD. Nunca llama a nadie: solo escucha, y por eso el bot de
// pruebas tiene que apuntar AQUÍ (NECTAR_BASE_URL) y jamás al Néctar real.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"io"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/nectar"
)

// maxRecords acota la memoria: el stub es de humo local, no de estrés.
const maxRecords = 500

// record es una llamada del bot al stub, tal cual llegó.
type record struct {
	At     time.Time       `json:"at"`
	Route  string          `json:"route"`
	Method string          `json:"method"`
	Path   string          `json:"path"`
	Body   json.RawMessage `json:"body,omitempty"`
}

type stub struct {
	latency       time.Duration
	serviceKey    string
	requestSecret string
	scheme        string
	msgSeq        atomic.Int64

	mu      sync.Mutex
	records []record
}

func main() {
	addr := flag.String("addr", ":9999", "dirección donde escuchar")
	latency := flag.Duration("latency", 0, "latencia artificial antes de responder")
	serviceKey := flag.String("service-key", os.Getenv("NECTAR_SERVICE_KEY"), "X-Bot-Service-Key esperada (vacío = no se verifica)")
	requestSecret := flag.String("request-secret", os.Getenv("NECTAR_REQUEST_SECRET"), "secreto para verificar la firma X-Nectar-* de las llamadas del bot (vacío = no se verifica)")
	scheme := flag.String("scheme", "http", "esquema con el que el bot marca a este stub (para reconstruir la URL firmada)")
	flag.Parse()

	s := &stub{latency: *latency, serviceKey: *serviceKey, requestSecret: *requestSecret, scheme: *scheme}
	srv := &http.Server{Addr: *addr, Handler: s.routes(), ReadHeaderTimeout: 10 * time.Second}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		slog.Info("nectar-stub escuchando", "addr", *addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("nectar-stub: %v", err)
		}
	}()
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}

func (s *stub) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/bot/conversations/{id}/state", s.handle("state", s.handleState))
	mux.HandleFunc("GET /api/bot/conversations/{id}/availability", s.handle("availability", s.handleAvailability))
	mux.HandleFunc("POST /api/bot/conversations/resolve", s.handle("resolve", s.handleResolve))
	mux.HandleFunc("POST /api/bot/conversations/{id}/messages", s.handle("messages", s.handleMessages))
	mux.HandleFunc("POST /api/bot/conversations/{id}/close", s.handle("close", s.handleClose))
	mux.HandleFunc("POST /api/bot/conversations/{id}/escalate", s.handle("escalate", s.handleEscalate))
	mux.HandleFunc("GET /api/bot/attachments/{id}", s.handle("attachments", s.handleAttachment))
	mux.HandleFunc("GET /records", s.handleRecords)
	mux.HandleFunc("DELETE /records", s.handleClearRecords)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	return mux
}

// handle envuelve una ruta: aplica la latencia, verifica credenciales (si hay secreto) y,
// solo si pasan, guarda la llamada. Una llamada mal firmada NO se registra: no es del bot.
func (s *stub) handle(route string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.latency > 0 {
			time.Sleep(s.latency)
		}
		body := readAndRestoreBody(r)
		if s.serviceKey != "" && r.Header.Get(nectar.ServiceKeyHeader) != s.serviceKey {
			http.Error(w, "clave de servicio invalida", http.StatusUnauthorized)
			return
		}
		if s.requestSecret != "" {
			signedURL := s.scheme + "://" + r.Host + r.URL.RequestURI()
			if err := nectar.VerifyNotice(s.requestSecret, r.Header.Get(nectar.SignatureHeader),
				r.Header.Get(nectar.TimestampHeader), signedURL, body); err != nil {
				//nolint:gosec // stub local: el dato es la URL que el bot firmó, justo lo que hay que cotejar
				slog.Warn("nectar-stub: firma invalida", "route", route, "error", err, "url", signedURL)
				http.Error(w, "firma invalida: "+err.Error(), http.StatusUnauthorized)
				return
			}
		}
		s.add(record{At: time.Now(), Route: route, Method: r.Method, Path: r.URL.Path, Body: validJSONOrNil(body)})
		next(w, r)
	}
}

func (s *stub) add(rec record) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.records) >= maxRecords {
		s.records = s.records[1:]
	}
	s.records = append(s.records, rec)
}

// validJSONOrNil evita que un cuerpo no-JSON rompa la serialización de /records.
func validJSONOrNil(b []byte) json.RawMessage {
	if len(b) == 0 || !json.Valid(b) {
		return nil
	}
	return json.RawMessage(b)
}

// readAndRestoreBody lee el cuerpo (la firma se calcula sobre su hash) y lo deja en r.Body.
func readAndRestoreBody(r *http.Request) []byte {
	if r.Body == nil {
		return nil
	}
	data, _ := io.ReadAll(r.Body)
	_ = r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(data))
	return data
}

func (s *stub) handleState(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, nectar.State{State: "bot", WindowOpen: true})
}

func (s *stub) handleAvailability(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, nectar.Availability{Available: true})
}

func (s *stub) handleResolve(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Phone string `json:"phone"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	writeJSON(w, nectar.Resolved{ConversationID: stableID(in.Phone), ChannelID: 1})
}

func (s *stub) handleMessages(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, struct {
		ID int64 `json:"id"`
	}{ID: s.msgSeq.Add(1)})
}

func (s *stub) handleClose(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }

func (s *stub) handleEscalate(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, nectar.EscalateResult{State: "escalated", Transitioned: true})
}

func (s *stub) handleAttachment(w http.ResponseWriter, _ *http.Request) {
	http.Error(w, "adjunto inexistente (stub sin media)", http.StatusNotFound)
}

// handleRecords devuelve lo recibido, opcionalmente filtrado por ?route=messages|escalate|…
func (s *stub) handleRecords(w http.ResponseWriter, r *http.Request) {
	route := r.URL.Query().Get("route")
	s.mu.Lock()
	out := make([]record, 0, len(s.records))
	for _, rec := range s.records {
		if route == "" || rec.Route == route {
			out = append(out, rec)
		}
	}
	s.mu.Unlock()
	writeJSON(w, map[string]any{"total": len(out), "records": out})
}

func (s *stub) handleClearRecords(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	s.records = nil
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// stableID arma un conversation_id ESTABLE a partir del teléfono: mismo teléfono, misma
// conversación, como haría el Néctar real. Siempre positivo.
func stableID(s string) int64 {
	var h uint64 = 1469598103934665603
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= 1099511628211
	}
	return int64(h%1_000_000_000) + 1
}
