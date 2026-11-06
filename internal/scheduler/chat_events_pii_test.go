package scheduler

import (
	"context"
	"errors"
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
	batches  int
	cutoffs  []time.Time
	startErr error
}

func (f *fakePIIMasker) PIIMaskStart(context.Context) (int64, error) { return f.start, f.startErr }

func (f *fakePIIMasker) MaskPIIBatch(ctx context.Context, afterID int64, cutoff time.Time, limit int) (int64, int64, error) {
	f.batches++
	f.cutoffs = append(f.cutoffs, cutoff)
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

// C-04: la tarea recorre por lotes desde donde quedó la marca hasta que no queda nada viejo, con el
// corte de retención a 30 días.
func TestMaskOldChatEventsPII_WalksBatchesUntilNoProgress(t *testing.T) {
	f := &fakePIIMasker{start: 100, rows: 100 + 2*chatEventsPIIBatchSize + 7}
	tasks := &Tasks{ChatEventsPII: f}
	before := time.Now()
	if err := tasks.maskOldChatEventsPII(context.Background()); err != nil {
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

func TestMaskOldChatEventsPII_BatchErrorStopsAndReports(t *testing.T) {
	f := &fakePIIMasker{rows: 10 * chatEventsPIIBatchSize, failAt: 2}
	if err := (&Tasks{ChatEventsPII: f}).maskOldChatEventsPII(context.Background()); err == nil {
		t.Fatal("un lote fallido debe reportarse")
	}
	if f.batches != 2 {
		t.Fatalf("tras el fallo no arranca otro lote: %d", f.batches)
	}

	f = &fakePIIMasker{startErr: errors.New("mysql caído")}
	if err := (&Tasks{ChatEventsPII: f}).maskOldChatEventsPII(context.Background()); err == nil || f.batches != 0 {
		t.Fatalf("sin punto de partida no hay lotes: err=%v lotes=%d", err, f.batches)
	}
}

// El plazo de la pasada solo impide arrancar el lote siguiente; lo que falta sigue mañana.
func TestMaskOldChatEventsPII_PassDeadlineStopsStartingBatches(t *testing.T) {
	f := &fakePIIMasker{rows: 1000 * chatEventsPIIBatchSize, delay: 20 * time.Millisecond}
	tasks := &Tasks{ChatEventsPII: f, piiPassTimeout: 50 * time.Millisecond}
	if err := tasks.maskOldChatEventsPII(context.Background()); err != nil {
		t.Fatalf("una pasada vencida no es un fallo de la tarea: %v", err)
	}
	if f.batches == 0 || f.batches > 5 {
		t.Fatalf("lotes = %d: debía cortar al vencer el plazo", f.batches)
	}
}

func TestMaskOldChatEventsPII_NoMaskerIsNoop(t *testing.T) {
	if err := (&Tasks{}).maskOldChatEventsPII(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestRegisterAll_IncludesChatEventsPIIRetention(t *testing.T) {
	s := NewScheduler(time.UTC)
	(&Tasks{}).RegisterAll(s)
	for _, task := range s.tasks {
		if task.Name == "chat_events_pii_retention" {
			if task.Weekdays != nil || task.Catchup != CatchupAlways {
				t.Fatalf("debe correr todos los días y recuperarse al arrancar: %+v", task)
			}
			return
		}
	}
	t.Fatal("falta la tarea chat_events_pii_retention")
}
