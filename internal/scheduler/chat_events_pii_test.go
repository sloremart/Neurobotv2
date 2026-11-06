package scheduler

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakePIIMasker simula chat_events: ids 1..rows ya pasaron la retención; cada lote avanza hasta
// limit filas y devuelve el último id enmascarado (afterID si no había nada).
type fakePIIMasker struct {
	start    int64
	rows     int64
	failAt   int // número de lote que falla (1 = el primero; 0 = ninguno)
	delay    time.Duration
	onBatch  func(n int) // se llama al empezar cada lote
	batches  int
	cutoffs  []time.Time
	startErr error
}

func (f *fakePIIMasker) PIIMaskStart(context.Context) (int64, error) { return f.start, f.startErr }

func (f *fakePIIMasker) MaskPIIBatch(ctx context.Context, afterID int64, cutoff time.Time, limit int) (int64, int64, error) {
	f.batches++
	f.cutoffs = append(f.cutoffs, cutoff)
	if f.onBatch != nil {
		f.onBatch(f.batches)
	}
	if f.batches == f.failAt {
		return afterID, 0, errors.New("mysql caído")
	}
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	if ctx.Err() != nil {
		return afterID, 0, ctx.Err()
	}
	last := min(afterID+int64(limit), f.rows)
	if last <= afterID {
		return afterID, 0, nil
	}
	return last, last - afterID, nil
}

// captureLogs cambia el logger por defecto por uno que escribe en un buffer (desde Debug).
func captureLogs(t *testing.T) func() string {
	t.Helper()
	var mu sync.Mutex
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&lockedWriter{w: &buf, mu: &mu}, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return func() string {
		mu.Lock()
		defer mu.Unlock()
		return buf.String()
	}
}

// fastPII es una tarea de retención con la pausa entre lotes mínima, para las pruebas.
func fastPII(f *fakePIIMasker) *Tasks {
	return &Tasks{ChatEventsPII: f, piiBatchPause: time.Nanosecond}
}

// C-04: la tarea recorre por lotes desde donde quedó la marca hasta que no queda nada viejo, con el
// corte de retención a 30 días.
func TestMaskOldChatEventsPII_WalksBatchesUntilNoProgress(t *testing.T) {
	f := &fakePIIMasker{start: 100, rows: 100 + 2*chatEventsPIIBatchSize + 7}
	before := time.Now()
	if err := fastPII(f).maskOldChatEventsPII(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.batches != 4 { // 2 lotes llenos + 1 parcial + el que ya no avanza
		t.Fatalf("lotes = %d, want 4", f.batches)
	}
	want := before.AddDate(0, 0, -chatEventsPIIRetentionDays)
	if d := f.cutoffs[0].Sub(want); d < -time.Minute || d > time.Minute {
		t.Fatalf("corte = %v, want ~%v", f.cutoffs[0], want)
	}
}

// Un error real de la base corta la pasada y se reporta (task_failed y Error).
func TestMaskOldChatEventsPII_BatchErrorStopsAndReports(t *testing.T) {
	f := &fakePIIMasker{rows: 10 * chatEventsPIIBatchSize, failAt: 2}
	if err := fastPII(f).maskOldChatEventsPII(context.Background()); err == nil {
		t.Fatal("un lote fallido debe reportarse")
	}
	if f.batches != 2 {
		t.Fatalf("tras el fallo no arranca otro lote: %d", f.batches)
	}

	f = &fakePIIMasker{startErr: errors.New("mysql caído")}
	if err := fastPII(f).maskOldChatEventsPII(context.Background()); err == nil || f.batches != 0 {
		t.Fatalf("sin punto de partida no hay lotes: err=%v lotes=%d", err, f.batches)
	}
}

// El plazo de la pasada solo impide arrancar el lote siguiente; lo que falta sigue mañana. Es lo
// esperado mientras se recorre el histórico: Info, sin Error (no sube a Telegram) ni fallo de tarea.
func TestMaskOldChatEventsPII_PassDeadlineIsInfoNotFailure(t *testing.T) {
	logs := captureLogs(t)
	f := &fakePIIMasker{rows: 1000 * chatEventsPIIBatchSize, delay: 20 * time.Millisecond}
	tasks := fastPII(f)
	tasks.piiPassTimeout = 50 * time.Millisecond
	if err := tasks.maskOldChatEventsPII(context.Background()); err != nil {
		t.Fatalf("una pasada vencida no es un fallo de la tarea: %v", err)
	}
	if f.batches == 0 || f.batches > 5 {
		t.Fatalf("lotes = %d: debía cortar al vencer el plazo", f.batches)
	}
	out := logs()
	if strings.Contains(out, "level=ERROR") || strings.Contains(out, "level=WARN") {
		t.Fatalf("la pasada vencida no es Error ni Warn:\n%s", out)
	}
	if !strings.Contains(out, "level=INFO") || !strings.Contains(out, "sigue mañana") {
		t.Fatalf("la pasada vencida deja un Info «sigue mañana»:\n%s", out)
	}
}

// El apagado a mitad de la pasada es un Warn y no un fallo de la tarea.
func TestMaskOldChatEventsPII_ShutdownIsWarnNotFailure(t *testing.T) {
	logs := captureLogs(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := &fakePIIMasker{rows: 1000 * chatEventsPIIBatchSize, onBatch: func(n int) {
		if n == 2 {
			cancel()
		}
	}}
	if err := fastPII(f).maskOldChatEventsPII(ctx); err != nil {
		t.Fatalf("el apagado no es un fallo de la tarea: %v", err)
	}
	if f.batches != 2 {
		t.Fatalf("tras el apagado no arranca otro lote: %d", f.batches)
	}
	out := logs()
	if strings.Contains(out, "level=ERROR") || !strings.Contains(out, "level=WARN") {
		t.Fatalf("el apagado deja un Warn y ningún Error:\n%s", out)
	}
}

// Entre lotes hay una pausa corta para no encadenar UPDATEs.
func TestMaskOldChatEventsPII_PausesBetweenBatches(t *testing.T) {
	f := &fakePIIMasker{rows: 3 * chatEventsPIIBatchSize}
	tasks := &Tasks{ChatEventsPII: f, piiBatchPause: 30 * time.Millisecond}
	start := time.Now()
	if err := tasks.maskOldChatEventsPII(context.Background()); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed < 3*30*time.Millisecond {
		t.Fatalf("4 lotes con 3 pausas de 30 ms tardaron %v", elapsed)
	}
	if chatEventsPIIBatchPause <= 0 {
		t.Fatal("la pausa por defecto debe ser positiva")
	}
}

func TestMaskOldChatEventsPII_NoMaskerIsNoop(t *testing.T) {
	if err := (&Tasks{}).maskOldChatEventsPII(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// Corre todos los días y su catch-up al arrancar solo de noche: un arranque de día espera a las 02:30.
func TestRegisterAll_IncludesChatEventsPIIRetention(t *testing.T) {
	s := NewScheduler(time.UTC)
	(&Tasks{}).RegisterAll(s)
	for _, task := range s.tasks {
		if task.Name == "chat_events_pii_retention" {
			if task.Weekdays != nil || task.Catchup != CatchupNighttime {
				t.Fatalf("debe correr todos los días y recuperarse solo de noche: %+v", task)
			}
			return
		}
	}
	t.Fatal("falta la tarea chat_events_pii_retention")
}
