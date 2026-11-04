package telegram

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// R1-4: si un error se repitió dentro de la ventana y no vuelve, la limpieza publica el conteo
// pendiente antes de borrar la entrada; si no hay conteo, la borra sin avisar.
func TestCleanupDedup_FlushesPendingRepeats(t *testing.T) {
	inner := slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug})
	h := NewAlertHandler(inner, &Client{})
	record := func(msg string) slog.Record { return slog.NewRecord(time.Now(), slog.LevelError, msg, 0) }

	for i := 0; i < 4; i++ {
		_ = h.Handle(context.Background(), record("siesa timeout"))
	}
	_ = h.Handle(context.Background(), record("otro error"))
	for len(h.ch) > 0 {
		<-h.ch
	}
	h.mu.Lock()
	for k, e := range h.dedup {
		e.sent = e.sent.Add(-2 * dedupWindow)
		h.dedup[k] = e
	}
	h.mu.Unlock()

	h.cleanupDedup()
	if len(h.ch) != 1 {
		t.Fatalf("solo el error con repeticiones pendientes deja un aviso de cierre, en cola %d", len(h.ch))
	}
	if msg := <-h.ch; !strings.Contains(msg, "siesa timeout") || !strings.Contains(msg, "3 veces") {
		t.Errorf("el aviso de cierre dice qué error y cuántas repeticiones: %q", msg)
	}
	if len(h.dedup) != 0 {
		t.Errorf("las entradas vencidas se borran, quedan %d", len(h.dedup))
	}
}

// Con el canal lleno el conteo no se pierde: la entrada queda para la próxima limpieza.
func TestCleanupDedup_FullChannelKeepsPendingRepeats(t *testing.T) {
	inner := slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug})
	h := NewAlertHandler(inner, &Client{})
	h.dedup["hash"] = dedupEntry{sent: time.Now().Add(-2 * dedupWindow), body: "siesa timeout", suppressed: 1}
	for i := 0; i < channelSize; i++ {
		h.ch <- "filler"
	}

	h.cleanupDedup()
	if len(h.dedup) != 1 {
		t.Fatalf("con el canal lleno la entrada se conserva, quedan %d", len(h.dedup))
	}
	for len(h.ch) > 0 {
		<-h.ch
	}

	h.cleanupDedup()
	if len(h.dedup) != 0 || len(h.ch) != 1 {
		t.Fatalf("con sitio se publica y se borra: entradas %d, en cola %d", len(h.dedup), len(h.ch))
	}
	if msg := <-h.ch; !strings.Contains(msg, "1 vez") {
		t.Errorf("el aviso de cierre dice la repetición en singular: %q", msg)
	}
}
