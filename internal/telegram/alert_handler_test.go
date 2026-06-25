package telegram

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestAlertHandler_ChannelFull_DoesNotBlock(t *testing.T) {
	inner := slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug})
	handler := NewAlertHandler(inner, &Client{})

	// Fill the channel completely
	for i := 0; i < channelSize; i++ {
		handler.ch <- "filler"
	}

	// This should NOT block — the default case drops the alert
	done := make(chan struct{})
	go func() {
		record := slog.NewRecord(time.Now(), slog.LevelError, "overflow test", 0)
		_ = handler.Handle(context.Background(), record)
		close(done)
	}()

	select {
	case <-done:
		// OK — Handle returned without blocking
	case <-time.After(2 * time.Second):
		t.Fatal("Handle() blocked when channel is full")
	}

	if len(handler.ch) != channelSize {
		t.Errorf("expected channel still full (%d), got %d", channelSize, len(handler.ch))
	}
}

func TestAlertHandler_DedupPreventsRepeat(t *testing.T) {
	inner := slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug})
	handler := NewAlertHandler(inner, &Client{})

	record := slog.NewRecord(time.Now(), slog.LevelError, "duplicate test", 0)

	// First call should enqueue
	_ = handler.Handle(context.Background(), record)
	if len(handler.ch) != 1 {
		t.Errorf("expected 1 message in channel, got %d", len(handler.ch))
	}

	// Second identical call should be deduped
	_ = handler.Handle(context.Background(), record)
	if len(handler.ch) != 1 {
		t.Errorf("expected still 1 message (deduped), got %d", len(handler.ch))
	}
}

// TestRedactValue_UTF8Safe verifica el fix N13: el enmascarado opera sobre runas, no bytes,
// para no partir caracteres UTF-8 multibyte (cédulas son ASCII, pero nombres llevan acentos).
func TestRedactValue_UTF8Safe(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"corto se oculta del todo", "1234", "***"},
		{"cedula ascii", "1000000689", "10***89"},
		{"nombre con acento al borde", "Iván", "***"}, // 4 runas → totalmente oculto
		{"nombre largo con acentos", "José Núñez", "Jo***ez"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := redactValue(c.in)
			if got != c.want {
				t.Errorf("redactValue(%q) = %q, want %q", c.in, got, c.want)
			}
			// Invariante clave: la salida SIEMPRE es UTF-8 válido (nunca runas partidas).
			if !utf8.ValidString(got) {
				t.Errorf("redactValue(%q) produjo UTF-8 inválido: %q", c.in, got)
			}
		})
	}
}

// TestFormatAttr_RedactsSensitiveKeys verifica que las claves PII se enmascaran y las no
// sensibles pasan tal cual (N-2 / redacción Ley 1581).
func TestFormatAttr_RedactsSensitiveKeys(t *testing.T) {
	sensitive := formatAttr(slog.String("doc", "1000000689"))
	if strings.Contains(sensitive, "1000000689") {
		t.Errorf("clave sensible 'doc' no fue enmascarada: %q", sensitive)
	}
	normal := formatAttr(slog.String("appointment_id", "7159"))
	if !strings.Contains(normal, "7159") {
		t.Errorf("clave no sensible 'appointment_id' no debe enmascararse: %q", normal)
	}
}

// TestRedactPII_MasksDigitRuns verifica el fix K3: secuencias de 6+ dígitos (cédulas/teléfonos)
// se enmascaran aunque vengan embebidas en texto libre, no solo en claves reconocidas.
func TestRedactPII_MasksDigitRuns(t *testing.T) {
	cases := []struct {
		name, in, mustNotContain, mustContain string
	}{
		{"cedula embebida en mensaje", "patient_create_failed doc=1000000689 entidad=EPS005", "1000000689", "EPS005"},
		{"telefono en texto", "no contesta el 3001234567", "3001234567", ""},
		{"numero corto (<6) no se toca", "agenda 7159 ok", "", "7159"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := redactPII(c.in)
			if c.mustNotContain != "" && strings.Contains(got, c.mustNotContain) {
				t.Errorf("redactPII(%q) = %q; NO debía contener %q", c.in, got, c.mustNotContain)
			}
			if c.mustContain != "" && !strings.Contains(got, c.mustContain) {
				t.Errorf("redactPII(%q) = %q; debía contener %q", c.in, got, c.mustContain)
			}
		})
	}
}

// TestFormatAttr_RedactsEmbeddedPII_UnlistedKey verifica K3: una clave NO listada cuyo valor
// trae un documento embebido también se enmascara (defensa en profundidad).
func TestFormatAttr_RedactsEmbeddedPII_UnlistedKey(t *testing.T) {
	out := formatAttr(slog.String("error", "insert sis_paci failed for 1000000689"))
	if strings.Contains(out, "1000000689") {
		t.Errorf("PII embebida en clave no listada no fue enmascarada: %q", out)
	}
}

// TestFormatMessage_RedactsPIIInMessage verifica K3: el documento que viaja en el propio
// r.Message (no en un attr) se redacta antes de enviar a Telegram.
func TestFormatMessage_RedactsPIIInMessage(t *testing.T) {
	inner := slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug})
	h := NewAlertHandler(inner, &Client{})
	record := slog.NewRecord(time.Now(), slog.LevelError, "patient_not_found doc 1000000689", 0)
	out := h.formatMessage(record)
	if strings.Contains(out, "1000000689") {
		t.Errorf("formatMessage no redactó el documento del mensaje: %q", out)
	}
}

func TestAlertHandler_BelowError_NotSent(t *testing.T) {
	inner := slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug})
	handler := NewAlertHandler(inner, &Client{})

	record := slog.NewRecord(time.Now(), slog.LevelWarn, "warning only", 0)
	_ = handler.Handle(context.Background(), record)

	if len(handler.ch) != 0 {
		t.Errorf("expected 0 messages for WARN level, got %d", len(handler.ch))
	}
}
