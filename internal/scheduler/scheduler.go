package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"
)

// defaultTaskTimeout es el backstop por-ejecución (N-39): acota una tarea colgada sin matar las
// corridas largas legítimas (los recordatorios diarios sobre volúmenes reales tardan minutos).
const defaultTaskTimeout = 1 * time.Hour

// RunRepo persists the last successful execution of scheduled tasks.
type RunRepo interface {
	GetLastRun(ctx context.Context, taskName string) (time.Time, error)
	SetLastRun(ctx context.Context, taskName string, at time.Time) error
}

// ScheduledTask represents a task that runs at a specific time.
type ScheduledTask struct {
	Name     string
	Hour     int
	Minute   int
	Weekdays []time.Weekday // nil = every day
	Fn       func(ctx context.Context) error
}

// Scheduler executes tasks at configured times using a 1-minute ticker.
type Scheduler struct {
	tasks       []ScheduledTask
	timezone    *time.Location
	runRepo     RunRepo // optional — persists run times for crash catch-up
	wg          sync.WaitGroup
	taskTimeout time.Duration
}

// NewScheduler creates a new scheduler with the given timezone.
func NewScheduler(tz *time.Location) *Scheduler {
	return &Scheduler{timezone: tz, taskTimeout: defaultTaskTimeout}
}

// SetRunRepo injects the repo for persisting task run times.
func (s *Scheduler) SetRunRepo(repo RunRepo) {
	s.runRepo = repo
}

// SetTaskTimeout sobreescribe el backstop por-ejecución (N-39). <=0 lo ignora.
func (s *Scheduler) SetTaskTimeout(d time.Duration) {
	if d > 0 {
		s.taskTimeout = d
	}
}

// Wait drena las tareas en vuelo hasta `timeout`. Se llama en el apagado, tras cancelar el ctx
// (las tareas paran rápido por N-38), para que ninguna goroutine muera a media escritura ni se
// pierda un last-run ya completado. Devuelve true si todas drenaron a tiempo.
func (s *Scheduler) Wait(timeout time.Duration) bool {
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		slog.Warn("scheduler: in-flight tasks did not drain within timeout", "timeout", timeout)
		return false
	}
}

// runTask ejecuta una tarea en su propia goroutine, trackeada por el WaitGroup (N-37) y acotada
// por un timeout por-ejecución (N-39). Persiste el last-run solo si la tarea terminó con éxito.
func (s *Scheduler) runTask(ctx context.Context, task ScheduledTask, runAt time.Time, label string) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			if r := recover(); r != nil {
				slog.Error("PANIC in "+label,
					"task", task.Name,
					"error", fmt.Sprintf("%v", r),
					"stack", string(debug.Stack()),
				)
			}
		}()

		// N-39: timeout por-ejecución como backstop. Hereda la cancelación del ctx de la app,
		// así que en el apagado la tarea para rápido (sleeps cancelables, N-38).
		timeout := s.taskTimeout
		if timeout <= 0 {
			timeout = defaultTaskTimeout // robustez ante construcción por literal
		}
		taskCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()

		slog.Info(label+" starting", "task", task.Name)
		start := time.Now()
		if err := task.Fn(taskCtx); err != nil {
			slog.Error(label+" failed", "task", task.Name, "error", err,
				"duration_ms", time.Since(start).Milliseconds())
			return
		}
		slog.Info(label+" completed", "task", task.Name,
			"duration_ms", time.Since(start).Milliseconds())

		// Persistir el last-run con un ctx INDEPENDIENTE y acotado: una tarea que SÍ terminó debe
		// registrar su corrida aunque el apagado esté cancelando el ctx de la app, para no
		// re-ejecutarse (y re-notificar) en el siguiente arranque.
		if s.runRepo != nil {
			// WithoutCancel: deriva del ctx de la app (conserva valores) pero NO se cancela en el
			// apagado, así una tarea que terminó persiste su last-run para no re-ejecutarse.
			pctx, pcancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			if err := s.runRepo.SetLastRun(pctx, task.Name, runAt); err != nil {
				slog.Error("persist "+label+" run", "task", task.Name, "error", err)
			}
			pcancel()
		}
	}()
}

// AddTask registers a task to be executed at the configured time.
func (s *Scheduler) AddTask(task ScheduledTask) {
	s.tasks = append(s.tasks, task)
}

// Start begins the scheduler loop. Blocks until ctx is cancelled.
func (s *Scheduler) Start(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	lastRun := make(map[string]time.Time)

	for {
		select {
		case <-ctx.Done():
			slog.Info("scheduler stopped")
			return
		case <-ticker.C:
			now := time.Now().In(s.timezone)
			s.evaluateTasks(ctx, now, lastRun)
		}
	}
}

// RunMissedTasks checks if any tasks should have run today but haven't.
// Only catches up tasks from the current day to avoid running stale historical tasks.
// Call once at startup, before Start().
func (s *Scheduler) RunMissedTasks(ctx context.Context) {
	if s.runRepo == nil {
		return
	}

	now := time.Now().In(s.timezone)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, s.timezone)

	for _, task := range s.tasks {
		// Check weekday filter
		if task.Weekdays != nil {
			found := false
			for _, wd := range task.Weekdays {
				if wd == now.Weekday() {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}

		// Only catch up if the scheduled time has already passed today
		scheduledToday := time.Date(now.Year(), now.Month(), now.Day(), task.Hour, task.Minute, 0, 0, s.timezone)
		if now.Before(scheduledToday) {
			continue
		}

		// Check DB for last run
		lastRun, err := s.runRepo.GetLastRun(ctx, task.Name)
		if err != nil {
			slog.Error("check missed task", "task", task.Name, "error", err)
			continue
		}

		// If last run was before today, the task was missed
		if !lastRun.Before(today) {
			continue // already ran today
		}

		slog.Warn("scheduler catch-up: missed task detected",
			"task", task.Name,
			"last_run", lastRun,
			"scheduled_at", scheduledToday,
		)

		s.runTask(ctx, task, time.Now(), "scheduler catch-up")
	}
}

// evaluateTasks checks all registered tasks against the given time and runs matching ones.
// lastRun is updated in place to prevent double execution within the same minute.
func (s *Scheduler) evaluateTasks(ctx context.Context, now time.Time, lastRun map[string]time.Time) []string {
	var executed []string

	for _, task := range s.tasks {
		if now.Hour() != task.Hour || now.Minute() != task.Minute {
			continue
		}

		// Check weekday filter
		if task.Weekdays != nil {
			found := false
			for _, wd := range task.Weekdays {
				if wd == now.Weekday() {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}

		// Prevent double execution within same minute
		key := task.Name
		if last, ok := lastRun[key]; ok && now.Sub(last) < 2*time.Minute {
			continue
		}
		lastRun[key] = now
		executed = append(executed, task.Name)

		s.runTask(ctx, task, now, "scheduler task")
	}

	return executed
}
