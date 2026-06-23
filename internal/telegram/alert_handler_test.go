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

func TestAlertHandler_BelowError_NotSent(t *testing.T) {
	inner := slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug})
	handler := NewAlertHandler(inner, &Client{})

	record := slog.NewRecord(time.Now(), slog.LevelWarn, "warning only", 0)
	_ = handler.Handle(context.Background(), record)

	if len(handler.ch) != 0 {
		t.Errorf("expected 0 messages for WARN level, got %d", len(handler.ch))
	}
}
