package telegram

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log"
	"log/slog"
	"strings"
	"sync"
	"time"
)

const (
	dedupWindow = 5 * time.Minute
	channelSize = 100
)

// AlertHandler wraps another slog.Handler and dispatches ERROR-level logs
// to Telegram asynchronously via a buffered channel.
type AlertHandler struct {
	inner  slog.Handler
	client *Client
	ch     chan string

	mu    sync.Mutex
	dedup map[string]time.Time

	// attrs and group collected via WithAttrs/WithGroup
	attrs []slog.Attr
	group string
}

// NewAlertHandler creates an AlertHandler that delegates to inner and sends
// ERROR logs to Telegram. Call Start() in a goroutine before use.
func NewAlertHandler(inner slog.Handler, client *Client) *AlertHandler {
	return &AlertHandler{
		inner:  inner,
		client: client,
		ch:     make(chan string, channelSize),
		dedup:  make(map[string]time.Time),
	}
}

// Start reads from the alert channel and sends messages to Telegram.
// Blocks until ctx is cancelled.
func (h *AlertHandler) Start(ctx context.Context) {
	// Periodic dedup map cleanup
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// Drain remaining messages
			for {
				select {
				case msg := <-h.ch:
					_ = h.client.SendMessage(context.Background(), msg)
				default:
					return
				}
			}
		case msg := <-h.ch:
			_ = h.client.SendMessage(ctx, msg)
		case <-ticker.C:
			h.cleanupDedup()
		}
	}
}

// Enabled delegates to the inner handler.
func (h *AlertHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

// Handle processes a log record: always delegates to inner, and if ERROR
// level, formats and dispatches to Telegram channel.
func (h *AlertHandler) Handle(ctx context.Context, r slog.Record) error {
	// Always delegate to inner handler first
	err := h.inner.Handle(ctx, r)

	// Only alert on ERROR level
	if r.Level < slog.LevelError {
		return err
	}

	msg := h.formatMessage(r)
	if msg == "" {
		return err
	}

	// Dedup check
	hash := h.hashMessage(msg)
	h.mu.Lock()
	if lastSent, ok := h.dedup[hash]; ok && time.Since(lastSent) < dedupWindow {
		h.mu.Unlock()
		return err
	}
	h.dedup[hash] = time.Now()
	h.mu.Unlock()

	// Non-blocking send to channel
	select {
	case h.ch <- msg:
	default:
		// Channel full, drop alert (don't block the application).
		// Use log.Println instead of slog to avoid infinite recursion (this IS the slog handler).
		log.Println("WARN: telegram alert channel full, dropping alert")
	}

	return err
}

// WithAttrs returns a new handler with additional attributes.
func (h *AlertHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &AlertHandler{
		inner:  h.inner.WithAttrs(attrs),
		client: h.client,
		ch:     h.ch,
		dedup:  h.dedup,
		attrs:  append(h.attrs, attrs...),
		group:  h.group,
	}
}

// WithGroup returns a new handler with the given group name.
func (h *AlertHandler) WithGroup(name string) slog.Handler {
	return &AlertHandler{
		inner:  h.inner.WithGroup(name),
		client: h.client,
		ch:     h.ch,
		dedup:  h.dedup,
		attrs:  h.attrs,
		group:  name,
	}
}

func (h *AlertHandler) formatMessage(r slog.Record) string {
	var b strings.Builder

	b.WriteString("<b>ERROR | neuro-bot</b>\n")
	b.WriteString("━━━━━━━━━━━━━━━━━\n")
	b.WriteString(escapeHTML(r.Message))
	b.WriteString("\n")

	// Collect attributes
	var details []string

	// Pre-handler attrs
	for _, a := range h.attrs {
		details = append(details, formatAttr(a))
	}

	// Record attrs
	r.Attrs(func(a slog.Attr) bool {
		details = append(details, formatAttr(a))
		return true
	})

	if len(details) > 0 {
		b.WriteString("\n<b>Detalles:</b>\n")
		for _, d := range details {
			b.WriteString("• ")
			b.WriteString(d)
			b.WriteString("\n")
		}
	}

	b.WriteString("\n<i>")
	b.WriteString(r.Time.Format("2006-01-02 15:04:05"))
	b.WriteString("</i>")

	return b.String()
}

// sensitiveAttrKeys son claves de log con PII de salud (Ley 1581) que NO deben salir en
// claro a un chat externo de Telegram. Se redactan antes de enviar. Es el mismo criterio
// que utils.MaskPhone para teléfonos, extendido a documento/nombre.
var sensitiveAttrKeys = map[string]bool{
	"doc": true, "cedula": true, "cédula": true, "document": true, "documento": true,
	"num_id": true, "numid": true, "numero_documento": true, "patient_doc": true,
	"name": true, "nombre": true, "nombres": true, "patient_name": true, "paciente": true,
	"phone": true, "telefono": true, "teléfono": true, "celular": true,
}

// redactValue enmascara un valor dejando solo extremos como pista (p.ej. "10***89").
// Opera sobre runas (no bytes) para no partir caracteres UTF-8 multibyte (ej. "Iván").
func redactValue(s string) string {
	r := []rune(s)
	if len(r) <= 4 {
		return "***"
	}
	return string(r[:2]) + "***" + string(r[len(r)-2:])
}

func formatAttr(a slog.Attr) string {
	val := a.Value.String()
	if sensitiveAttrKeys[strings.ToLower(a.Key)] {
		val = redactValue(val)
	}
	return fmt.Sprintf("<code>%s</code>: %s", escapeHTML(a.Key), escapeHTML(val))
}

func escapeHTML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

func (h *AlertHandler) hashMessage(msg string) string {
	sum := sha256.Sum256([]byte(msg))
	return fmt.Sprintf("%x", sum[:8])
}

func (h *AlertHandler) cleanupDedup() {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now()
	for k, t := range h.dedup {
		if now.Sub(t) > dedupWindow {
			delete(h.dedup, k)
		}
	}
}
