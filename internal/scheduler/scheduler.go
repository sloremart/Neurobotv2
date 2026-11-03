package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/observability"
)

// defaultTaskTimeout es el backstop por-ejecución (N-39): acota una tarea colgada sin matar las
// corridas largas legítimas (los recordatorios diarios sobre volúmenes reales tardan minutos).
const defaultTaskTimeout = 1 * time.Hour

// ErrNothingSent marca el error de una tarea que falló ANTES de su primer envío (p. ej. SIESA no
// respondió al leer las citas). Solo ese error se reintenta: un reintento tras un fallo parcial
// volvería a cobrar los envíos ya hechos.
var ErrNothingSent = errors.New("nothing sent")

// Reintento acotado de una tarea que falló antes de enviar (D-03): cada taskRetryInterval y a lo
// sumo taskMaxRetries veces tras la corrida de su hora.
const (
	taskRetryInterval = 5 * time.Minute
	taskMaxRetries    = 6
)

// noRetry es el intento de una corrida que no se reintenta (el catch-up del arranque, que ya tiene
// su propia política horaria L14).
const noRetry = -1

// pendingRetry es el próximo reintento programado de una tarea: attempt es su número (1..taskMaxRetries).
type pendingRetry struct {
	task    ScheduledTask
	attempt int
	due     time.Time
}

// RunRepo persists the last successful execution of scheduled tasks.
type RunRepo interface {
	GetLastRun(ctx context.Context, taskName string) (time.Time, error)
	SetLastRun(ctx context.Context, taskName string, at time.Time) error
}

// ScheduledTask represents a task that runs at a specific time.
// CatchupMode define si una tarea perdida puede recuperarse al arrancar y bajo qué condición (L14).
type CatchupMode int

// Modos de catch-up de una tarea perdida al arrancar (L14).
const (
	CatchupAlways  CatchupMode = iota // default: recupera a cualquier hora (tareas internas)
	CatchupDaytime                    // recupera solo en ventana diurna (notificaciones WhatsApp)
	CatchupNever                      // nunca recupera (IVR: solo a su hora, tras el WA matutino)
)

// Ventana diurna permitida para el catch-up de notificaciones al paciente [start, end) (L14).
const (
	catchupDaytimeStartHour = 7
	catchupDaytimeEndHour   = 20
)

type ScheduledTask struct {
	Name     string
	Hour     int
	Minute   int
	Weekdays []time.Weekday // nil = every day
	Fn       func(ctx context.Context) error
	Catchup  CatchupMode // política de recuperación al arranque (L14); zero-value = CatchupAlways
}

// catchupAllowed decide si una tarea perdida puede recuperarse a la hora dada (L14).
func catchupAllowed(mode CatchupMode, hour int) bool {
	switch mode {
	case CatchupNever:
		return false
	case CatchupDaytime:
		return hour >= catchupDaytimeStartHour && hour < catchupDaytimeEndHour
	default: // CatchupAlways
		return true
	}
}

// Scheduler executes tasks at configured times using a 1-minute ticker.
type Scheduler struct {
	tasks       []ScheduledTask
	timezone    *time.Location
	runRepo     RunRepo // optional — persists run times for crash catch-up
	wg          sync.WaitGroup
	taskTimeout time.Duration

	retryMu sync.Mutex
	retries map[string]pendingRetry // por nombre de tarea (D-03)
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
// attempt es 0 en la corrida de su hora, 1..taskMaxRetries en un reintento y noRetry en el catch-up.
func (s *Scheduler) runTask(ctx context.Context, task ScheduledTask, runAt time.Time, label string, attempt int) {
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
		trace := observability.TraceTask(task.Name, runAt.Format("20060102"))
		if err := task.Fn(taskCtx); err != nil {
			dur := time.Since(start).Milliseconds()
			slog.Error(label+" failed", "task", task.Name, "error", err,
				"duration_ms", dur, "attempt", attempt)
			attrs := map[string]interface{}{"duration_ms": dur, "attempt": attempt}
			if s.scheduleRetry(ctx, task, runAt, attempt, err) {
				attrs["retries_exhausted"] = true
			}
			observability.Emit(trace, "scheduler", "task_failed", observability.EmitOpts{
				Reason: err.Error(),
				Attrs:  attrs,
			})
			return
		}
		dur := time.Since(start).Milliseconds()
		slog.Info(label+" completed", "task", task.Name,
			"duration_ms", dur)
		observability.Emit(trace, "scheduler", "task_completed", observability.EmitOpts{
			Attrs: map[string]interface{}{"duration_ms": dur},
		})

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

// scheduleRetry programa el reintento de una tarea que falló (D-03) y devuelve true si se agotaron
// los reintentos. Solo se reintenta un error marcado con ErrNothingSent (nada salió: reintentar no
// duplica envíos), nunca en el apagado (ctx cancelado) ni en el catch-up. El reintento lo lanza
// evaluateTasks en el tick que cumple `due`: no hay timers ni goroutines que drenar.
func (s *Scheduler) scheduleRetry(ctx context.Context, task ScheduledTask, runAt time.Time, attempt int, err error) bool {
	if attempt == noRetry || ctx.Err() != nil || !errors.Is(err, ErrNothingSent) {
		return false
	}
	if attempt >= taskMaxRetries {
		slog.Error("scheduler task retries exhausted", "task", task.Name,
			"retries", taskMaxRetries, "error", err)
		return true
	}
	s.retryMu.Lock()
	defer s.retryMu.Unlock()
	if s.retries == nil {
		s.retries = make(map[string]pendingRetry)
	}
	s.retries[task.Name] = pendingRetry{task: task, attempt: attempt + 1, due: runAt.Add(taskRetryInterval)}
	slog.Warn("scheduler task retry scheduled", "task", task.Name,
		"attempt", attempt+1, "due", runAt.Add(taskRetryInterval))
	return false
}

// takeDueRetries saca los reintentos vencidos a `now`.
func (s *Scheduler) takeDueRetries(now time.Time) []pendingRetry {
	s.retryMu.Lock()
	defer s.retryMu.Unlock()
	var due []pendingRetry
	for name, r := range s.retries {
		if !now.Before(r.due) {
			due = append(due, r)
			delete(s.retries, name)
		}
	}
	return due
}

// dropRetry descarta el reintento pendiente de una tarea (su corrida de hora lo reemplaza).
func (s *Scheduler) dropRetry(name string) {
	s.retryMu.Lock()
	delete(s.retries, name)
	s.retryMu.Unlock()
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

		// L14: política de catch-up. El IVR (CatchupNever) solo corre a su hora (13:00, como
		// seguimiento del WA matutino); recuperarlo tarde llamaría a pacientes fuera de hora y, si el
		// WA acaba de recuperarse en el mismo arranque, justo después del WhatsApp (sin ventana de
		// respuesta). Las notificaciones WA (CatchupDaytime) solo se recuperan en horario diurno.
		if !catchupAllowed(task.Catchup, now.Hour()) {
			slog.Warn("scheduler catch-up: skipped by policy",
				"task", task.Name, "mode", task.Catchup, "hour", now.Hour())
			continue
		}

		slog.Warn("scheduler catch-up: missed task detected",
			"task", task.Name,
			"last_run", lastRun,
			"scheduled_at", scheduledToday,
		)

		s.runTask(ctx, task, time.Now(), "scheduler catch-up", noRetry)
	}
}

// evaluateTasks checks all registered tasks against the given time and runs matching ones.
// lastRun is updated in place to prevent double execution within the same minute.
func (s *Scheduler) evaluateTasks(ctx context.Context, now time.Time, lastRun map[string]time.Time) []string {
	var executed []string

	// D-03: reintentos vencidos de tareas que fallaron antes de enviar. En el apagado no se lanzan.
	if ctx.Err() == nil {
		for _, r := range s.takeDueRetries(now) {
			executed = append(executed, r.task.Name)
			s.runTask(ctx, r.task, now, "scheduler retry", r.attempt)
		}
	}

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

		s.dropRetry(task.Name)
		s.runTask(ctx, task, now, "scheduler task", 0)
	}

	return executed
}
