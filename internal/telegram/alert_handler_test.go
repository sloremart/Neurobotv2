package telegram

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
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

// TestFormatMessage_ShowsTraceID verifica que el trace_id de una anomalía aparece en la alerta de
// Telegram (Fase 2) para saltar directo al recorrido por /flow-trace.
func TestFormatMessage_ShowsTraceID(t *testing.T) {
	inner := slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug})
	h := NewAlertHandler(inner, &Client{})
	record := slog.NewRecord(time.Now(), slog.LevelError, "reconcile anomalies detected", 0)
	record.Add("trace_id", "wl:123", "count", 2)
	out := h.formatMessage(record)
	if !strings.Contains(out, "wl:123") {
		t.Errorf("trace_id no aparece en la alerta: %q", out)
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

// m11: la alerta lleva la hora al pie, pero la deduplicación no debe depender de ella; si no,
// el mismo error repetido un segundo después vuelve a salir a Telegram.
func TestAlertHandler_DedupIgnoresTime(t *testing.T) {
	inner := slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug})
	handler := NewAlertHandler(inner, &Client{})

	first := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		record := slog.NewRecord(first.Add(time.Duration(i)*time.Second), slog.LevelError, "pass deadline exceeded", 0)
		record.Add("task", "reminders")
		_ = handler.Handle(context.Background(), record)
	}
	if len(handler.ch) != 1 {
		t.Fatalf("el mismo error en segundos distintos debe salir una vez, salieron %d", len(handler.ch))
	}
	if msg := <-handler.ch; !strings.Contains(msg, "2026-11-04 10:00:00") {
		t.Errorf("la alerta enviada conserva la hora al pie: %q", msg)
	}

	// Alertas distintas (otro attr) no se pierden.
	other := slog.NewRecord(first, slog.LevelError, "pass deadline exceeded", 0)
	other.Add("task", "waiting_list")
	_ = handler.Handle(context.Background(), other)
	if len(handler.ch) != 1 {
		t.Errorf("una alerta con otros detalles es distinta y debe salir, en cola %d", len(handler.ch))
	}
}

// Los loggers derivados (slog.With → WithAttrs) comparten el mapa de dedup: deben compartir
// también su cerrojo, o dos goroutines escriben el mapa a la vez (lo detecta -race).
func TestAlertHandler_DerivedHandlersShareDedupLock(t *testing.T) {
	inner := slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug})
	base := NewAlertHandler(inner, &Client{})
	derived := []slog.Handler{base.WithAttrs([]slog.Attr{slog.String("component", "a")}), base.WithGroup("g")}

	var wg sync.WaitGroup
	for i, h := range derived {
		wg.Add(1)
		go func(i int, h slog.Handler) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				r := slog.NewRecord(time.Now(), slog.LevelError, fmt.Sprintf("error %d-%d", i, j), 0)
				_ = h.Handle(context.Background(), r)
				<-base.ch
			}
		}(i, h)
	}
	wg.Wait()
	if len(base.dedup) != 100 {
		t.Errorf("las 100 alertas distintas quedan en el mismo mapa de dedup, hay %d", len(base.dedup))
	}
}

// C-16: el cuerpo HTTP incrustado en un error (Bird o Néctar devuelven el contacto con su nombre)
// no sale a Telegram; se conservan el contexto y el status, y el resto se recorta.
func TestFormatAttr_OmitsHTTPBodyInErrors(t *testing.T) {
	out := formatAttr(slog.String("error",
		`bird api error: status 422, body: {"code":"InvalidPayload","contact":{"displayName":"María López"}}`))
	if strings.Contains(out, "María") || strings.Contains(out, "displayName") {
		t.Errorf("el cuerpo del error salió a Telegram: %q", out)
	}
	if !strings.Contains(out, "status 422") {
		t.Errorf("el contexto y el status se conservan: %q", out)
	}

	long := formatAttr(slog.String("error", strings.Repeat("contexto ", 60)+"María López"))
	if strings.Contains(long, "María") {
		t.Errorf("un valor largo se recorta antes de salir a Telegram: %q", long)
	}
	if n := utf8.RuneCountInString(long); n > maxAttrRunes+60 {
		t.Errorf("el valor recortado ocupa %d runas", n)
	}
}
