package telegram

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/neuro-bot/neuro-bot/internal/utils"
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

	// mu y dedup se comparten con los handlers derivados (WithAttrs/WithGroup): el cerrojo es un
	// puntero para que todos protejan el mismo mapa.
	mu    *sync.Mutex
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
		mu:     &sync.Mutex{},
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

	body := h.formatBody(r)
	if body == "" {
		return err
	}

	// Dedup check: sobre la alerta sin la hora, para que el mismo error repetido segundos después
	// no vuelva a salir. Otro mensaje u otros detalles dan otro hash y sí salen.
	hash := h.hashMessage(body)
	h.mu.Lock()
	if lastSent, ok := h.dedup[hash]; ok && time.Since(lastSent) < dedupWindow {
		h.mu.Unlock()
		return err
	}
	h.dedup[hash] = time.Now()
	h.mu.Unlock()

	// Non-blocking send to channel
	select {
	case h.ch <- withTimestamp(body, r.Time):
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
		mu:     h.mu,
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
		mu:     h.mu,
		dedup:  h.dedup,
		attrs:  h.attrs,
		group:  name,
	}
}

// formatMessage es la alerta completa, tal como sale a Telegram.
func (h *AlertHandler) formatMessage(r slog.Record) string {
	return withTimestamp(h.formatBody(r), r.Time)
}

// withTimestamp añade la hora del record al pie de la alerta.
func withTimestamp(body string, t time.Time) string {
	return body + "\n<i>" + t.Format("2006-01-02 15:04:05") + "</i>"
}

// formatBody arma la alerta sin la hora: es lo que se deduplica.
func (h *AlertHandler) formatBody(r slog.Record) string {
	var b strings.Builder

	b.WriteString("<b>ERROR | neuro-bot</b>\n")
	b.WriteString("━━━━━━━━━━━━━━━━━\n")
	// K3: el propio mensaje puede llevar PII inline (cédula/teléfono) que no está en un attr
	// con clave reconocible → redactar secuencias largas de dígitos antes de enviar a Telegram.
	b.WriteString(escapeHTML(redactPII(r.Message)))
	b.WriteString("\n")

	// trace_id prominente (Fase 2 observabilidad): permite saltar directo al recorrido completo
	// con GET /api/internal/flow-trace?trace_id=<...>. No es PII.
	if tid := traceIDFromRecord(h.attrs, r); tid != "" {
		b.WriteString("<b>trace:</b> <code>")
		b.WriteString(escapeHTML(tid))
		b.WriteString("</code>\n")
	}

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

	return b.String()
}

// alertOnlySensitiveKeys son las claves que, además de las de la política común
// (utils.IsSensitiveLogKey), no salen en claro a Telegram: cuerpos que en stdout se conservan
// para diagnosticar los errores de Bird y Néctar (Ley 1581, K3).
var alertOnlySensitiveKeys = map[string]bool{"content": true, "body": true}

// isSensitiveAlertKey dice si el valor de la clave se redacta entero antes de enviarlo.
func isSensitiveAlertKey(key string) bool {
	return utils.IsSensitiveLogKey(key) || alertOnlySensitiveKeys[strings.ToLower(key)]
}

// digitRunRe captura secuencias de 6+ dígitos (cédulas, teléfonos) que podrían ser PII,
// aunque vengan embebidas en un mensaje o en el valor de una clave no listada (K3).
var digitRunRe = regexp.MustCompile(`\d{6,}`)

// redactPII enmascara secuencias largas de dígitos dentro de un texto libre, dejando los
// extremos como pista. No toca claves estructuradas (esas se redactan por nombre en formatAttr).
func redactPII(s string) string {
	return digitRunRe.ReplaceAllStringFunc(s, redactValue)
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

// maxAttrRunes es el largo máximo de un valor en la alerta; el valor entero queda en el log.
const maxAttrRunes = 160

// trimForAlert quita el cuerpo HTTP que algunos errores incrustan (Bird y Néctar responden JSON que
// puede repetir el contacto con su nombre) y recorta el resto a maxAttrRunes (C-16).
func trimForAlert(s string) string {
	if i := strings.Index(s, "{"); i >= 0 {
		s = s[:i] + fmt.Sprintf("[cuerpo omitido: %d caracteres]", utf8.RuneCountInString(s[i:]))
	}
	if r := []rune(s); len(r) > maxAttrRunes {
		s = string(r[:maxAttrRunes]) + "…"
	}
	return s
}

const (
	// modulePrefix identifica los frames del propio bot en un stack de Go.
	modulePrefix = "github.com/neuro-bot/neuro-bot/"
	// maxStackRunes acota el stack de un PANIC; el cliente corta además el mensaje entero.
	maxStackRunes = 1500
)

// stackForAlert reduce el stack de un PANIC a la cabecera de la goroutine y los frames del módulo
// (función + archivo:línea), que es lo que ubica el fallo. Sin frames del módulo lo deja entero.
// No lleva PII: son rutas y direcciones (H1).
func stackForAlert(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	kept := lines[:1]
	for i := 1; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], modulePrefix) {
			kept = append(kept, lines[i])
			if i+1 < len(lines) {
				kept = append(kept, lines[i+1])
			}
		}
	}
	if len(kept) > 1 {
		s = strings.Join(kept, "\n")
	}
	if r := []rune(s); len(r) > maxStackRunes {
		s = string(r[:maxStackRunes]) + "…"
	}
	return s
}

func formatAttr(a slog.Attr) string {
	val := a.Value.String()
	switch {
	case a.Key == "stack":
		val = redactPII(stackForAlert(val))
	case isSensitiveAlertKey(a.Key):
		// Clave reconocida como sensible → redactar el valor completo.
		val = redactValue(val)
	default:
		// K3: claves no listadas todavía pueden traer PII embebida (p.ej. un documento dentro
		// de un mensaje de error) → enmascarar secuencias largas de dígitos por si acaso.
		val = redactPII(trimForAlert(val))
	}
	return fmt.Sprintf("<code>%s</code>: %s", escapeHTML(a.Key), escapeHTML(val))
}

// traceIDFromRecord extrae el atributo "trace_id" del record (o de los attrs pre-cargados).
func traceIDFromRecord(pre []slog.Attr, r slog.Record) string {
	for _, a := range pre {
		if a.Key == "trace_id" {
			return a.Value.String()
		}
	}
	var tid string
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == "trace_id" {
			tid = a.Value.String()
			return false
		}
		return true
	})
	return tid
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
