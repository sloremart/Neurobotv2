package utils

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func capturePassLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// D-07: una pasada colgada vence en su plazo y deja UNA línea de error con el nombre del bucle.
func TestRunPass_ExpiredPassLogsOneError(t *testing.T) {
	logs := capturePassLogs(t)
	expired := RunPass(context.Background(), "test-loop", 10*time.Millisecond, func(ctx context.Context) {
		<-ctx.Done()
	})
	if !expired {
		t.Fatal("RunPass debió informar la pasada vencida")
	}
	out := strings.TrimSpace(logs.String())
	if strings.Count(out, "\n") != 0 || !strings.Contains(out, "level=ERROR") || !strings.Contains(out, "loop=test-loop") {
		t.Fatalf("logs = %q, want una sola línea ERROR con loop=test-loop", out)
	}
}

// Una pasada que termina a tiempo, o que corta el apagado, no es una pasada vencida.
func TestRunPass_NoErrorOnSuccessOrShutdown(t *testing.T) {
	logs := capturePassLogs(t)
	if RunPass(context.Background(), "ok", time.Second, func(ctx context.Context) {
		if _, ok := ctx.Deadline(); !ok {
			t.Error("la pasada debe correr con plazo")
		}
	}) {
		t.Error("una pasada a tiempo no está vencida")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if RunPass(ctx, "shutdown", time.Second, func(ctx context.Context) { <-ctx.Done() }) {
		t.Error("el apagado no es una pasada vencida")
	}
	if logs.Len() != 0 {
		t.Fatalf("logs = %q, want nada", logs.String())
	}
}

// Sin plazo explícito rige BackgroundPassTimeout.
func TestRunPass_DefaultTimeout(t *testing.T) {
	RunPass(context.Background(), "default", 0, func(ctx context.Context) {
		dl, ok := ctx.Deadline()
		if !ok || time.Until(dl) > BackgroundPassTimeout || time.Until(dl) < BackgroundPassTimeout-time.Minute {
			t.Errorf("plazo = %v, want ~%v", time.Until(dl), BackgroundPassTimeout)
		}
	})
}
