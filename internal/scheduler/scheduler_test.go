package scheduler

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
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

	// Third call with old lastRun entry → should execute again
	lastRun["once-per-minute"] = now.Add(-3 * time.Minute)
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
