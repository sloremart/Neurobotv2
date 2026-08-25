package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/neuro-bot/neuro-bot/internal/services"
	"github.com/neuro-bot/neuro-bot/internal/session"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
)

// TestSessionWriter es el subconjunto del repo de sesión que usa el harness de test.
type TestSessionWriter interface {
	FindActiveByPhone(ctx context.Context, phone string) (*session.Session, error)
	SetContextBatch(ctx context.Context, sessionID string, kvs map[string]string) error
	ResumeSession(ctx context.Context, sessionID, newState string, timeoutMinutes int) error
}

// TestVirtualEnqueuer encola un "mensaje virtual" para que el worker procese la sesión.
type TestVirtualEnqueuer interface {
	EnqueueVirtual(phone string)
}

// TestHandler expone endpoints SOLO para pruebas locales (gated por E2E_TEST_ENDPOINTS=1 en main).
// Nunca debe habilitarse en producción.
type TestHandler struct {
	sessions TestSessionWriter
	pool     TestVirtualEnqueuer
}

// NewTestHandler construye el handler de endpoints de prueba (ver TestHandler).
func NewTestHandler(s TestSessionWriter, p TestVirtualEnqueuer) *TestHandler {
	return &TestHandler{sessions: s, pool: p}
}

// HandleInjectCups inyecta CUPS en una sesión YA identificada (como si el OCR los hubiera leído) y
// dispara el procesamiento: fija ocr_cups_json + estado VALIDATE_OCR y encola un mensaje virtual. Así el
// harness salta el paso de subir la foto y ejercita el resto del pipeline real (validación, agrupamiento,
// cobertura, búsqueda de slots). Body: {"phone":"+57...","cups":"890274,883101:2"} (code[:cantidad]).
func (h *TestHandler) HandleInjectCups(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Phone string `json:"phone"`
		Cups  string `json:"cups"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Phone == "" || req.Cups == "" {
		http.Error(w, `{"error":"phone y cups requeridos"}`, http.StatusBadRequest)
		return
	}

	sess, err := h.sessions.FindActiveByPhone(r.Context(), req.Phone)
	if err != nil || sess == nil {
		http.Error(w, `{"error":"sesión activa no encontrada para ese teléfono"}`, http.StatusNotFound)
		return
	}

	entries := parseInjectCups(req.Cups)
	if len(entries) == 0 {
		http.Error(w, `{"error":"no se pudo parsear ningún CUP"}`, http.StatusBadRequest)
		return
	}
	cupsJSON, _ := json.Marshal(entries)

	if err := h.sessions.SetContextBatch(r.Context(), sess.ID, map[string]string{"ocr_cups_json": string(cupsJSON)}); err != nil {
		http.Error(w, `{"error":"set context failed"}`, http.StatusInternalServerError)
		return
	}
	if err := h.sessions.ResumeSession(r.Context(), sess.ID, sm.StateValidateOCR, 120); err != nil {
		http.Error(w, `{"error":"resume session failed"}`, http.StatusInternalServerError)
		return
	}
	h.pool.EnqueueVirtual(req.Phone)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"ok": true, "session_id": sess.ID, "state": sm.StateValidateOCR, "cups": entries,
	})
}

// parseInjectCups convierte "code[:qty],code[:qty]" en []services.CUPSEntry (qty default 1).
func parseInjectCups(s string) []services.CUPSEntry {
	var entries []services.CUPSEntry
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		code, qty := part, 1
		if i := strings.Index(part, ":"); i >= 0 {
			code = strings.TrimSpace(part[:i])
			if q, e := strconv.Atoi(strings.TrimSpace(part[i+1:])); e == nil && q > 0 {
				qty = q
			}
		}
		if code != "" {
			entries = append(entries, services.CUPSEntry{Code: code, Quantity: qty})
		}
	}
	return entries
}
