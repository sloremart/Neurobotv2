package scheduler

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/observability"
)

func TestNewScheduler(t *testing.T) {
	loc, _ := time.LoadLocation("America/Bogota")
	s := NewScheduler(loc)
	if s == nil {
		t.Fatal("expected non-nil scheduler")
	}
}

func TestAddTask(t *testing.T) {
	loc, _ := time.LoadLocation("UTC")
	s := NewScheduler(loc)
	s.AddTask(ScheduledTask{
		Name: "test-task",
		Hour: 10, Minute: 0,
		Fn: func(ctx context.Context) error { return nil },
	})
	if len(s.tasks) != 1 {
		t.Errorf("expected 1 task, got %d", len(s.tasks))
	}
}

func TestStart_CancelStops(t *testing.T) {
	loc, _ := time.LoadLocation("UTC")
	s := NewScheduler(loc)
	s.AddTask(ScheduledTask{
		Name: "dummy",
		Hour: 0, Minute: 0,
		Fn: func(ctx context.Context) error {
			return nil
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		s.Start(ctx)
		close(done)
	}()

	cancel()
	select {
	case <-done:
		// OK, Start returned
	case <-time.After(3 * time.Second):
		t.Fatal("Start did not return after cancel")
	}
}

func TestAddMultipleTasks(t *testing.T) {
	loc, _ := time.LoadLocation("UTC")
	s := NewScheduler(loc)
	s.AddTask(ScheduledTask{Name: "t1", Hour: 7, Minute: 0, Fn: func(ctx context.Context) error { return nil }})
	s.AddTask(ScheduledTask{Name: "t2", Hour: 8, Minute: 0, Fn: func(ctx context.Context) error { return nil }})
	s.AddTask(ScheduledTask{Name: "t3", Hour: 15, Minute: 0, Fn: func(ctx context.Context) error { return nil }})

	if len(s.tasks) != 3 {
		t.Errorf("expected 3 tasks, got %d", len(s.tasks))
	}
}

func TestSchedulerTimezone(t *testing.T) {
	loc, err := time.LoadLocation("America/Bogota")
	if err != nil {
		t.Skip("timezone not available")
	}
	s := NewScheduler(loc)
	if s.timezone != loc {
		t.Error("expected Bogota timezone")
	}
}

// --- evaluateTasks tests ---

func TestEvaluateTasks_MatchingTask(t *testing.T) {
	s := NewScheduler(time.UTC)
	var called atomic.Int32
	s.AddTask(ScheduledTask{
		Name: "match", Hour: 10, Minute: 30,
		Fn: func(ctx context.Context) error {
			called.Add(1)
			return nil
		},
	})

	now := time.Date(2026, 3, 16, 10, 30, 0, 0, time.UTC) // Monday 10:30
	lastRun := make(map[string]time.Time)

	executed := s.evaluateTasks(context.Background(), now, lastRun)

	if len(executed) != 1 || executed[0] != "match" {
		t.Errorf("expected [match], got %v", executed)
	}

	// Wait briefly for goroutine
	time.Sleep(50 * time.Millisecond)
	if called.Load() != 1 {
		t.Errorf("expected Fn called once, got %d", called.Load())
	}
}

func TestEvaluateTasks_NoMatch_WrongTime(t *testing.T) {
	s := NewScheduler(time.UTC)
	s.AddTask(ScheduledTask{
		Name: "task", Hour: 10, Minute: 30,
		Fn: func(ctx context.Context) error { return nil },
	})

	now := time.Date(2026, 3, 16, 11, 0, 0, 0, time.UTC)
	lastRun := make(map[string]time.Time)

	executed := s.evaluateTasks(context.Background(), now, lastRun)
	if len(executed) != 0 {
		t.Errorf("expected no executions, got %v", executed)
	}
}

func TestEvaluateTasks_WeekdayFilter_Matches(t *testing.T) {
	s := NewScheduler(time.UTC)
	s.AddTask(ScheduledTask{
		Name: "weekday-task", Hour: 8, Minute: 0,
		Weekdays: []time.Weekday{time.Monday, time.Wednesday, time.Friday},
		Fn:       func(ctx context.Context) error { return nil },
	})

	// Monday 8:00 → match
	mon := time.Date(2026, 3, 16, 8, 0, 0, 0, time.UTC)
	lastRun := make(map[string]time.Time)
	executed := s.evaluateTasks(context.Background(), mon, lastRun)
	if len(executed) != 1 {
		t.Error("expected Monday to match")
	}
}

func TestEvaluateTasks_WeekdayFilter_DoesNotMatch(t *testing.T) {
	s := NewScheduler(time.UTC)
	s.AddTask(ScheduledTask{
		Name: "weekday-task", Hour: 8, Minute: 0,
		Weekdays: []time.Weekday{time.Monday, time.Wednesday, time.Friday},
		Fn:       func(ctx context.Context) error { return nil },
	})

	// Tuesday 8:00 → no match
	tue := time.Date(2026, 3, 17, 8, 0, 0, 0, time.UTC)
	lastRun := make(map[string]time.Time)
	executed := s.evaluateTasks(context.Background(), tue, lastRun)
	if len(executed) != 0 {
		t.Error("expected Tuesday not to match weekday filter")
	}
}

func TestEvaluateTasks_PreventDoubleExecution(t *testing.T) {
	s := NewScheduler(time.UTC)
	var callCount atomic.Int32
	s.AddTask(ScheduledTask{
		Name: "once-per-minute", Hour: 10, Minute: 0,
		Fn: func(ctx context.Context) error {
			callCount.Add(1)
			return nil
		},
	})

	now := time.Date(2026, 3, 16, 10, 0, 0, 0, time.UTC)
	lastRun := make(map[string]time.Time)

	// First call → should execute
	executed1 := s.evaluateTasks(context.Background(), now, lastRun)
	if len(executed1) != 1 {
		t.Error("expected first call to execute")
	}

	// Second call same minute → should NOT execute (dedup)
	executed2 := s.evaluateTasks(context.Background(), now, lastRun)
	if len(executed2) != 0 {
		t.Error("expected second call within same minute to be deduped")
	}

	// Third call with lastRun from another day → should execute again
	lastRun["once-per-minute"] = now.AddDate(0, 0, -1)
	executed3 := s.evaluateTasks(context.Background(), now, lastRun)
	if len(executed3) != 1 {
		t.Error("expected execution after dedup window passed")
	}
}

func TestEvaluateTasks_MultipleTasks_OnlyMatchingRun(t *testing.T) {
	s := NewScheduler(time.UTC)
	s.AddTask(ScheduledTask{Name: "task-8", Hour: 8, Minute: 0, Fn: func(ctx context.Context) error { return nil }})
	s.AddTask(ScheduledTask{Name: "task-10", Hour: 10, Minute: 0, Fn: func(ctx context.Context) error { return nil }})
	s.AddTask(ScheduledTask{Name: "task-10b", Hour: 10, Minute: 0, Fn: func(ctx context.Context) error { return nil }})
	s.AddTask(ScheduledTask{Name: "task-15", Hour: 15, Minute: 0, Fn: func(ctx context.Context) error { return nil }})

	now := time.Date(2026, 3, 16, 10, 0, 0, 0, time.UTC)
	lastRun := make(map[string]time.Time)

	executed := s.evaluateTasks(context.Background(), now, lastRun)
	if len(executed) != 2 {
		t.Errorf("expected 2 tasks at 10:00, got %d: %v", len(executed), executed)
	}

	// Verify the right tasks ran
	names := map[string]bool{}
	for _, n := range executed {
		names[n] = true
	}
	if !names["task-10"] || !names["task-10b"] {
		t.Errorf("expected task-10 and task-10b, got %v", executed)
	}
}

func TestEvaluateTasks_NoWeekdayFilter_MatchesAnyDay(t *testing.T) {
	s := NewScheduler(time.UTC)
	s.AddTask(ScheduledTask{
		Name: "daily", Hour: 6, Minute: 0,
		Fn: func(ctx context.Context) error { return nil },
	})

	// Should match any day at 6:00
	for d := 15; d <= 21; d++ {
		now := time.Date(2026, 3, d, 6, 0, 0, 0, time.UTC)
		lastRun := make(map[string]time.Time)
		executed := s.evaluateTasks(context.Background(), now, lastRun)
		if len(executed) != 1 {
			t.Errorf("expected match for day %d (%s), got %v", d, now.Weekday(), executed)
		}
	}
}

func TestEvaluateTasks_TaskFnError_DoesNotCrash(t *testing.T) {
	s := NewScheduler(time.UTC)
	s.AddTask(ScheduledTask{
		Name: "error-task", Hour: 10, Minute: 0,
		Fn: func(ctx context.Context) error {
			return context.DeadlineExceeded
		},
	})

	now := time.Date(2026, 3, 16, 10, 0, 0, 0, time.UTC)
	lastRun := make(map[string]time.Time)

	executed := s.evaluateTasks(context.Background(), now, lastRun)
	if len(executed) != 1 {
		t.Error("expected task to be scheduled even if Fn returns error")
	}
	// Give goroutine time to run
	time.Sleep(50 * time.Millisecond)
}

// TestScheduler_WaitDrainsInFlightTasks cubre N-37: Wait() espera a que las tareas en vuelo
// terminen (con deadline), para que el apagado no las mate a medio camino.
func TestScheduler_WaitDrainsInFlightTasks(t *testing.T) {
	s := NewScheduler(time.UTC)
	var done atomic.Bool
	release := make(chan struct{})
	s.AddTask(ScheduledTask{
		Name: "slow", Hour: 10, Minute: 0,
		Fn: func(_ context.Context) error {
			<-release
			done.Store(true)
			return nil
		},
	})

	now := time.Date(2026, 3, 16, 10, 0, 0, 0, time.UTC)
	s.evaluateTasks(context.Background(), now, make(map[string]time.Time))

	// La tarea está bloqueada → Wait con deadline corto NO debe drenar.
	if s.Wait(50 * time.Millisecond) {
		t.Error("expected Wait to time out while task is in-flight")
	}
	if done.Load() {
		t.Error("task should still be blocked")
	}

	// Liberar y drenar.
	close(release)
	if !s.Wait(2 * time.Second) {
		t.Error("expected Wait to drain after task released")
	}
	if !done.Load() {
		t.Error("task should have completed after drain")
	}
}

func TestEvaluateTasks_EmptyTaskList(t *testing.T) {
	s := NewScheduler(time.UTC)
	now := time.Date(2026, 3, 16, 10, 0, 0, 0, time.UTC)
	lastRun := make(map[string]time.Time)

	executed := s.evaluateTasks(context.Background(), now, lastRun)
	if len(executed) != 0 {
		t.Errorf("expected no executions for empty task list, got %v", executed)
	}
}

func TestEvaluateTasks_AllWeekdaysMatch(t *testing.T) {
	s := NewScheduler(time.UTC)
	s.AddTask(ScheduledTask{
		Name:     "all-days",
		Hour:     2,
		Minute:   0,
		Weekdays: []time.Weekday{time.Sunday, time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday, time.Saturday},
		Fn:       func(ctx context.Context) error { return nil },
	})

	for d := 15; d <= 21; d++ {
		now := time.Date(2026, 3, d, 2, 0, 0, 0, time.UTC)
		lastRun := make(map[string]time.Time)
		executed := s.evaluateTasks(context.Background(), now, lastRun)
		if len(executed) != 1 {
			t.Errorf("expected match for day %d (%s)", d, now.Weekday())
		}
	}
}

func TestEvaluateTasks_ContextPassedToFn(t *testing.T) {
	s := NewScheduler(time.UTC)
	ctxKey := struct{}{}
	ctx := context.WithValue(context.Background(), ctxKey, "test-value")

	var gotValue string
	done := make(chan struct{})
	s.AddTask(ScheduledTask{
		Name: "ctx-task", Hour: 10, Minute: 0,
		Fn: func(c context.Context) error {
			if v, ok := c.Value(ctxKey).(string); ok {
				gotValue = v
			}
			close(done)
			return nil
		},
	})

	now := time.Date(2026, 3, 16, 10, 0, 0, 0, time.UTC)
	s.evaluateTasks(ctx, now, make(map[string]time.Time))

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Fn did not execute in time")
	}

	if gotValue != "test-value" {
		t.Errorf("expected context value 'test-value', got %q", gotValue)
	}
}

// --- reintento acotado de una tarea fallida (D-03) ---

// failingUntil devuelve una tarea a las 07:00 cuyo Fn falla con ErrNothingSent las primeras `fails`
// veces y luego tiene éxito; calls cuenta las ejecuciones.
func failingUntil(fails int32, calls *atomic.Int32) ScheduledTask {
	return ScheduledTask{
		Name: "reminders", Hour: 7, Minute: 0,
		Fn: func(_ context.Context) error {
			if calls.Add(1) <= fails {
				return fmt.Errorf("fetch tomorrow appointments: %w", ErrNothingSent)
			}
			return nil
		},
	}
}

// tickState guarda el mapa de cada scheduler de prueba: varias llamadas a tickUntil son el MISMO
// proceso, como en Start (un mapa nuevo haría ver una corrida de hoy como pendiente, D-14).
var tickState sync.Map

// tickUntil evalúa minuto a minuto desde `from` hasta `to` (inclusive), drenando la tarea en vuelo
// tras cada tick para que su resultado (y el reintento que programe) quede registrado.
func tickUntil(ctx context.Context, t *testing.T, s *Scheduler, from, to time.Time) {
	t.Helper()
	state, _ := tickState.LoadOrStore(s, make(map[string]time.Time))
	lastRun := state.(map[string]time.Time)
	for now := from; !now.After(to); now = now.Add(time.Minute) {
		s.evaluateTasks(ctx, now, lastRun)
		if !s.Wait(2 * time.Second) {
			t.Fatal("task did not drain")
		}
	}
}

func TestRetry_FailedTaskRetriesEvery5MinUntilSuccess(t *testing.T) {
	s := NewScheduler(time.UTC)
	var calls atomic.Int32
	s.AddTask(failingUntil(1, &calls))

	at := func(h, m int) time.Time { return time.Date(2026, 3, 16, h, m, 0, 0, time.UTC) }
	tickUntil(context.Background(), t, s, at(7, 0), at(7, 4))
	if got := calls.Load(); got != 1 {
		t.Fatalf("before 07:05: calls = %d, want 1", got)
	}
	tickUntil(context.Background(), t, s, at(7, 5), at(7, 5))
	if got := calls.Load(); got != 2 {
		t.Fatalf("at 07:05: calls = %d, want 2 (retry)", got)
	}
	// Tuvo éxito en el reintento: no hay más corridas en el resto de la mañana.
	tickUntil(context.Background(), t, s, at(7, 6), at(9, 0))
	if got := calls.Load(); got != 2 {
		t.Fatalf("after success: calls = %d, want 2", got)
	}
}

func TestRetry_GivesUpAfterMaxRetries(t *testing.T) {
	s := NewScheduler(time.UTC)
	var calls atomic.Int32
	s.AddTask(failingUntil(1000, &calls))

	tickUntil(context.Background(), t, s,
		time.Date(2026, 3, 16, 7, 0, 0, 0, time.UTC), time.Date(2026, 3, 16, 13, 0, 0, 0, time.UTC))
	if got, want := calls.Load(), int32(1+taskMaxRetries); got != want {
		t.Fatalf("calls = %d, want %d (1 run + %d retries)", got, want, taskMaxRetries)
	}
}

// Un error SIN ErrNothingSent (p. ej. falló tras enviar a parte de la lista) no se reintenta:
// el reintento volvería a cobrar los envíos ya hechos.
func TestRetry_PartialFailureIsNotRetried(t *testing.T) {
	s := NewScheduler(time.UTC)
	var calls atomic.Int32
	s.AddTask(ScheduledTask{
		Name: "partial", Hour: 7, Minute: 0,
		Fn: func(_ context.Context) error {
			calls.Add(1)
			return errors.New("send failed after 40 reminders")
		},
	})

	tickUntil(context.Background(), t, s,
		time.Date(2026, 3, 16, 7, 0, 0, 0, time.UTC), time.Date(2026, 3, 16, 8, 0, 0, 0, time.UTC))
	if got := calls.Load(); got != 1 {
		t.Fatalf("calls = %d, want 1 (no retry)", got)
	}
}

func TestRetry_CancelledContextDoesNotRetry(t *testing.T) {
	at := func(m int) time.Time { return time.Date(2026, 3, 16, 7, m, 0, 0, time.UTC) }

	// Falla con el ctx ya cancelado (apagado): no se programa reintento.
	s := NewScheduler(time.UTC)
	var calls atomic.Int32
	s.AddTask(failingUntil(1000, &calls))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tickUntil(ctx, t, s, at(0), at(0))
	tickUntil(context.Background(), t, s, at(1), at(40))
	if got := calls.Load(); got != 1 {
		t.Fatalf("failed under cancelled ctx: calls = %d, want 1", got)
	}

	// Reintento ya programado y luego apagado: el tick con el ctx cancelado no lo lanza.
	s2 := NewScheduler(time.UTC)
	var calls2 atomic.Int32
	s2.AddTask(failingUntil(1000, &calls2))
	ctx2, cancel2 := context.WithCancel(context.Background())
	tickUntil(ctx2, t, s2, at(0), at(0))
	cancel2()
	tickUntil(ctx2, t, s2, at(1), at(40))
	if got := calls2.Load(); got != 1 {
		t.Fatalf("retry due after shutdown: calls = %d, want 1", got)
	}
}

// La cadena de reintentos no pasa de taskRetryWindow desde la hora programada aunque queden
// intentos: same_day_reminders_16 nunca corre después de las 16:30 (no se solapa con la corrida
// siguiente ni sale de su ventana de negocio).
func TestRetry_NeverAfterWindow(t *testing.T) {
	at := func(h, m int) time.Time { return time.Date(2026, 3, 16, h, m, 0, 0, time.UTC) }
	newSched := func(calls *atomic.Int32) *Scheduler {
		s := NewScheduler(time.UTC)
		task := failingUntil(1000, calls)
		task.Name, task.Hour = "same_day_reminders_16", 16
		s.AddTask(task)
		return s
	}

	// Ticks que llegan tarde (16:20): los reintentos siguen cada 5 min solo hasta las 16:30.
	var calls atomic.Int32
	s := newSched(&calls)
	tickUntil(context.Background(), t, s, at(16, 0), at(16, 0))
	tickUntil(context.Background(), t, s, at(16, 20), at(16, 30))
	if got := calls.Load(); got != 4 { // 16:00, 16:20, 16:25, 16:30
		t.Fatalf("up to 16:30: calls = %d, want 4", got)
	}
	tickUntil(context.Background(), t, s, at(16, 31), at(17, 30))
	if got := calls.Load(); got != 4 {
		t.Fatalf("after 16:30: calls = %d, want 4 (no retry outside the window)", got)
	}

	// Un reintento vencido cuyo tick llega ya fuera de la ventana se descarta.
	var late atomic.Int32
	s2 := newSched(&late)
	tickUntil(context.Background(), t, s2, at(16, 0), at(16, 0))
	tickUntil(context.Background(), t, s2, at(16, 40), at(17, 30))
	if got := late.Load(); got != 1 {
		t.Fatalf("late tick: calls = %d, want 1", got)
	}
}

// fakeRunRepo registra los last-run persistidos.
type fakeRunRepo struct {
	mu   sync.Mutex
	sets []time.Time
}

func (f *fakeRunRepo) GetLastRun(context.Context, string) (time.Time, error) { return time.Time{}, nil }

func (f *fakeRunRepo) SetLastRun(_ context.Context, _ string, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sets = append(f.sets, at)
	return nil
}

func (f *fakeRunRepo) snapshot() []time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]time.Time(nil), f.sets...)
}

// Un reintento exitoso persiste el last-run (con la hora del reintento); el intento fallido no.
func TestRetry_SuccessPersistsLastRun(t *testing.T) {
	s := NewScheduler(time.UTC)
	repo := &fakeRunRepo{}
	s.SetRunRepo(repo)
	var calls atomic.Int32
	s.AddTask(failingUntil(1, &calls))

	at := func(m int) time.Time { return time.Date(2026, 3, 16, 7, m, 0, 0, time.UTC) }
	tickUntil(context.Background(), t, s, at(0), at(4))
	if got := repo.snapshot(); len(got) != 0 {
		t.Fatalf("failed attempt persisted last-run: %v", got)
	}
	tickUntil(context.Background(), t, s, at(5), at(10))
	if got := repo.snapshot(); len(got) != 1 || !got[0].Equal(at(5)) {
		t.Fatalf("last-run = %v, want [%v]", got, at(5))
	}
}

// El catch-up del arranque que falla no se reintenta: conserva su política horaria L14.
func TestRetry_FailedCatchupIsNotRetried(t *testing.T) {
	s := NewScheduler(time.UTC)
	s.SetRunRepo(&fakeRunRepo{}) // last-run cero: la tarea se da por perdida hoy
	var calls atomic.Int32
	task := failingUntil(1000, &calls)
	task.Hour = 0 // su hora de hoy ya pasó, sea cual sea la hora real
	s.AddTask(task)

	s.RunMissedTasks(context.Background())
	if !s.Wait(2 * time.Second) {
		t.Fatal("catch-up did not drain")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("catch-up calls = %d, want 1", got)
	}
	s.retryMu.Lock()
	pending := len(s.retries)
	s.retryMu.Unlock()
	if pending != 0 {
		t.Fatalf("failed catch-up scheduled %d retries, want 0", pending)
	}
}

// Un intento colgado (p. ej. SIESA sin responder hasta el backstop de 1 h) que termina fuera de la
// ventana no se reintenta, aunque le queden intentos.
func TestRetry_HungAttemptEndingOutsideWindowIsNotRetried(t *testing.T) {
	s := NewScheduler(time.UTC)
	task := ScheduledTask{Name: "voice_reminders", Hour: 13}
	scheduled := time.Date(2026, 3, 16, 13, 0, 0, 0, time.UTC)
	err := fmt.Errorf("fetch appointments for IVR: %w", ErrNothingSent)

	if !s.scheduleRetry(context.Background(), task, &retryChain{scheduledAt: scheduled}, scheduled.Add(time.Hour), err) {
		t.Error("expected the chain to give up")
	}
	if len(s.retries) != 0 {
		t.Errorf("retries = %v, want none", s.retries)
	}
	// El mismo fallo a tiempo sí se reintenta, 5 min después del minuto en que terminó.
	if s.scheduleRetry(context.Background(), task, &retryChain{scheduledAt: scheduled}, scheduled.Add(42*time.Second), err) {
		t.Error("in-window failure should be retried")
	}
	if r, ok := s.retries[task.Name]; !ok || !r.due.Equal(scheduled.Add(taskRetryInterval)) || r.chain.attempt != 1 {
		t.Errorf("retry = %+v, want attempt 1 due %v", r, scheduled.Add(taskRetryInterval))
	}
}

// --- arranque tardío tras un salto del reloj (D-14) ---

// reminderLate es una tarea de las 07:00 con la política dada; calls cuenta sus ejecuciones.
func reminderLate(mode CatchupMode, calls *atomic.Int32) ScheduledTask {
	return ScheduledTask{
		Name: "late", Hour: 7, Minute: 0, Catchup: mode,
		Fn: func(_ context.Context) error {
			calls.Add(1)
			return nil
		},
	}
}

// ticks evalúa en cada minuto dado (del 16-mar-2026 a las 06:00 + m) sobre el mismo mapa y drena.
func ticks(t *testing.T, s *Scheduler, lastRun map[string]time.Time, minutes ...int) {
	t.Helper()
	base := time.Date(2026, 3, 16, 6, 0, 0, 0, time.UTC)
	for _, m := range minutes {
		s.evaluateTasks(context.Background(), base.Add(time.Duration(m)*time.Minute), lastRun)
		if !s.Wait(2 * time.Second) {
			t.Fatal("task did not drain")
		}
	}
}

// El reloj salta por encima del minuto programado: la tarea corre en el primer tic posterior, una sola
// vez aunque sigan los tics del tope, y no vuelve a correr ni a su hora tras un retroceso del reloj.
func TestEvaluateTasks_ClockJump_RunsLateOnce(t *testing.T) {
	s := NewScheduler(time.UTC)
	var calls atomic.Int32
	s.AddTask(reminderLate(CatchupAlways, &calls))
	lastRun := map[string]time.Time{}

	ticks(t, s, lastRun, 59, 61) // 06:59 y 07:01: el minuto 07:00 no existió
	if got := calls.Load(); got != 1 {
		t.Fatalf("calls = %d, want 1 (arranque tardío)", got)
	}
	ticks(t, s, lastRun, 62, 65, 70, 60, 61) // el tope, y un retroceso del reloj hasta las 07:00
	if got := calls.Load(); got != 1 {
		t.Errorf("calls = %d, want 1: no corre dos veces el mismo día", got)
	}
}

// Justo en el tope corre; un minuto después ya no, ni en los tics siguientes.
func TestEvaluateTasks_LateStartCap(t *testing.T) {
	for _, tc := range []struct {
		name   string
		minute int
		want   int32
	}{
		{"a los 10 min corre", 70, 1},
		{"a los 11 min no corre", 71, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewScheduler(time.UTC)
			var calls atomic.Int32
			s.AddTask(reminderLate(CatchupAlways, &calls))
			ticks(t, s, map[string]time.Time{}, 59, tc.minute)
			if got := calls.Load(); got != tc.want {
				t.Errorf("calls = %d, want %d", got, tc.want)
			}
		})
	}
}

// El arranque tardío sigue la política de catch-up (L14); a su minuto exacto la tarea corre siempre.
func TestEvaluateTasks_LateStartRespectsCatchup(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mode   CatchupMode
		hour   int
		minute int // minutos desde las 06:00 del tic tardío
		want   int32
	}{
		{"CatchupNever no arranca tarde", CatchupNever, 7, 62, 0},
		{"CatchupDaytime en la ventana diurna", CatchupDaytime, 7, 62, 1},
		{"CatchupDaytime fuera de la ventana", CatchupDaytime, 6, 2, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewScheduler(time.UTC)
			var calls atomic.Int32
			task := reminderLate(tc.mode, &calls)
			task.Hour = tc.hour
			s.AddTask(task)
			ticks(t, s, map[string]time.Time{}, -1, tc.minute)
			if got := calls.Load(); got != tc.want {
				t.Errorf("calls = %d, want %d", got, tc.want)
			}
		})
	}

	s := NewScheduler(time.UTC)
	var calls atomic.Int32
	s.AddTask(reminderLate(CatchupNever, &calls))
	ticks(t, s, map[string]time.Time{}, 60)
	if got := calls.Load(); got != 1 {
		t.Errorf("a su minuto exacto CatchupNever corre: calls = %d", got)
	}
}

// El filtro de días vale también para el arranque tardío.
func TestEvaluateTasks_LateStartRespectsWeekday(t *testing.T) {
	s := NewScheduler(time.UTC)
	var calls atomic.Int32
	task := reminderLate(CatchupAlways, &calls)
	task.Weekdays = []time.Weekday{time.Sunday} // el 16-mar-2026 es lunes
	s.AddTask(task)
	ticks(t, s, map[string]time.Time{}, 61)
	if got := calls.Load(); got != 0 {
		t.Errorf("calls = %d, want 0", got)
	}
}

// Un reinicio minutos después de la hora no es un salto del reloj: lo programado antes del arranque ya
// lo decidió RunMissedTasks (corrió, ya había corrido o su política lo impide), así que el primer tic
// no lo relanza. Lo que todavía no tocaba al arrancar corre normal, también si el reloj salta.
func TestStartLastRun_RestartDoesNotRerun(t *testing.T) {
	s := NewScheduler(time.UTC)
	var calls atomic.Int32
	s.AddTask(reminderLate(CatchupAlways, &calls))

	restart := time.Date(2026, 3, 16, 7, 3, 0, 0, time.UTC)
	lastRun := s.startLastRun(restart)
	ticks(t, s, lastRun, 64, 65)
	if got := calls.Load(); got != 0 {
		t.Errorf("calls = %d, want 0 tras un reinicio posterior a la hora", got)
	}

	lastRun = s.startLastRun(restart.Add(-10 * time.Minute)) // 06:53
	ticks(t, s, lastRun, 59, 61)
	if got := calls.Load(); got != 1 {
		t.Errorf("calls = %d, want 1: arrancó antes de la hora", got)
	}

	// Al día siguiente lo sembrado ya no cuenta.
	lastRun = s.startLastRun(restart)
	s.evaluateTasks(context.Background(), restart.Add(24*time.Hour-3*time.Minute), lastRun)
	if !s.Wait(2 * time.Second) {
		t.Fatal("task did not drain")
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("calls = %d, want 2: corre a su hora al día siguiente", got)
	}
}

// El reintento (D-03) de un arranque tardío cuenta su ventana desde la hora programada, no desde el tic.
func TestEvaluateTasks_LateStartRetryWindowFromSchedule(t *testing.T) {
	s := NewScheduler(time.UTC)
	var calls atomic.Int32
	s.AddTask(failingUntil(100, &calls))
	lastRun := map[string]time.Time{}
	ticks(t, s, lastRun, 59, 70) // arranque tardío a las 07:10 que falla
	if got := calls.Load(); got != 1 {
		t.Fatalf("calls = %d, want 1", got)
	}
	s.retryMu.Lock()
	r, ok := s.retries["reminders"]
	s.retryMu.Unlock()
	if !ok {
		t.Fatal("el fallo debía programar un reintento")
	}
	if want := time.Date(2026, 3, 16, 7, 0, 0, 0, time.UTC); !r.chain.scheduledAt.Equal(want) {
		t.Errorf("scheduledAt = %v, want %v", r.chain.scheduledAt, want)
	}
}

// m15: los atributos de task_failed dicen qué intento fue, si el error era reintentable y si la cadena
// se rindió. El catch-up y un fallo tras enviar no se reintentan.
func TestTaskFailed_Attrs(t *testing.T) {
	sink := &observability.CaptureSink{}
	stop := observability.StartCapture(sink)

	s := NewScheduler(time.UTC)
	var calls atomic.Int32
	s.AddTask(failingUntil(1000, &calls))
	tickUntil(context.Background(), t, s,
		time.Date(2026, 3, 16, 7, 0, 0, 0, time.UTC), time.Date(2026, 3, 16, 8, 0, 0, 0, time.UTC))

	partial := NewScheduler(time.UTC)
	partial.AddTask(ScheduledTask{
		Name: "partial", Hour: 7, Minute: 0,
		Fn: func(_ context.Context) error { return errors.New("send failed after 40 reminders") },
	})
	tickUntil(context.Background(), t, partial,
		time.Date(2026, 3, 16, 7, 0, 0, 0, time.UTC), time.Date(2026, 3, 16, 7, 0, 0, 0, time.UTC))
	stop()

	var failed []map[string]interface{}
	for _, ev := range sink.Events() {
		if ev.Step == "task_failed" {
			failed = append(failed, ev.Attrs)
		}
	}
	if want := 1 + taskMaxRetries + 1; len(failed) != want {
		t.Fatalf("task_failed = %d, want %d", len(failed), want)
	}
	for i, attrs := range failed[:1+taskMaxRetries] {
		if attrs["attempt"] != i || attrs["retryable"] != true {
			t.Errorf("intento %d: attrs = %v", i, attrs)
		}
		if _, exhausted := attrs["retries_exhausted"]; exhausted != (i == taskMaxRetries) {
			t.Errorf("intento %d: retries_exhausted = %v", i, attrs)
		}
	}
	last := failed[len(failed)-1]
	if last["retryable"] != false || last["attempt"] != 0 {
		t.Errorf("fallo tras enviar: attrs = %v", last)
	}
	if _, exhausted := last["retries_exhausted"]; exhausted {
		t.Errorf("un fallo que no se reintenta no agota nada: %v", last)
	}
}

// I-1: en producción el ticker no cae en el segundo 0 del minuto. Un tic a las HH:MM:30 es el minuto
// normal de la tarea, no un arranque tardío: las CatchupNever (IVR, avisos del mismo día, re-enganche)
// corren, también si el tic anterior fue a las HH:MM-1:30.
func TestEvaluateTasks_MidMinuteTickIsOnTime(t *testing.T) {
	s := NewScheduler(time.UTC)
	(&Tasks{}).RegisterAll(s)
	for i := range s.tasks {
		s.tasks[i].Fn = func(context.Context) error { return nil }
	}
	lastRun := map[string]time.Time{}
	wednesday := func(h, m int) time.Time { return time.Date(2026, 3, 18, h, m, 30, 0, time.UTC) }
	for _, tc := range []struct {
		at   time.Time
		task string
	}{
		{wednesday(7, 5), "morning_reengage"},
		{wednesday(10, 0), "same_day_reminders_10"},
		{wednesday(13, 0), "voice_reminders"},
	} {
		s.evaluateTasks(context.Background(), tc.at.Add(-time.Minute), lastRun)
		got := s.evaluateTasks(context.Background(), tc.at, lastRun)
		if !slices.Contains(got, tc.task) {
			t.Errorf("%s: a las %s debía correr en su minuto, corrieron %v", tc.task, tc.at.Format("15:04:05"), got)
		}
	}
	if !s.Wait(2 * time.Second) {
		t.Fatal("tasks did not drain")
	}
}

// G1-M1: el cron que choca con una corrida manual en curso no sube un Error ni emite task_failed, y se
// reintenta a los 5 min como cualquier fallo antes de enviar.
func TestRunTask_ReminderClashIsWarnAndRetries(t *testing.T) {
	var logs bytes.Buffer
	var logMu sync.Mutex
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&lockedWriter{mu: &logMu, w: &logs}, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	sink := &observability.CaptureSink{}
	stop := observability.StartCapture(sink)

	s := NewScheduler(time.UTC)
	var calls atomic.Int32
	s.AddTask(ScheduledTask{
		Name: "reminders", Hour: 7, Minute: 0,
		Fn: func(_ context.Context) error {
			if calls.Add(1) == 1 {
				return nothingSent(ErrRemindersRunning)
			}
			return nil
		},
	})
	tickUntil(context.Background(), t, s,
		time.Date(2026, 3, 16, 7, 0, 0, 0, time.UTC), time.Date(2026, 3, 16, 7, 5, 0, 0, time.UTC))
	stop()

	if got := calls.Load(); got != 2 {
		t.Errorf("calls = %d, want 2 (el choque y su reintento)", got)
	}
	if n := sink.CountStep("scheduler", "task_failed"); n != 0 {
		t.Errorf("task_failed = %d, want 0", n)
	}
	logMu.Lock()
	out := logs.String()
	logMu.Unlock()
	if strings.Contains(out, "level=ERROR") || !strings.Contains(out, "otra corrida en curso") {
		t.Errorf("el choque va en Warn, sin Error: %s", out)
	}
	if !strings.Contains(out, "retry=true") { // con cadena y sin apagado el Warn dice que SÍ se reintenta
		t.Errorf("el Warn debe decir que se reintenta: %s", out)
	}
}
