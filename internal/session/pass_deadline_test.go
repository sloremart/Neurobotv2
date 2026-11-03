package session

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// D-07: con MySQL colgado (la consulta no vuelve hasta que se cancele su ctx), la pasada del
// checker termina al vencer su plazo y la siguiente arranca: el atomic.Bool no queda tomado.
func TestRunInactivityPass_HungQueryEndsAtDeadline(t *testing.T) {
	var inactiveCalls, escalatedCalls atomic.Int32
	repo := &mockRepo{
		findInactiveFn: func(ctx context.Context, _ int) ([]InactiveSession, error) {
			inactiveCalls.Add(1)
			<-ctx.Done()
			return nil, ctx.Err()
		},
		findEscalatedFn: func(ctx context.Context) ([]EscalatedSession, error) {
			escalatedCalls.Add(1)
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	mgr := NewSessionManager(repo, 120)
	mgr.passTimeout = 20 * time.Millisecond
	deps := InactivityDeps{ReminderMin: 5, CloseMin: 15}

	for pass := 1; pass <= 2; pass++ {
		done := make(chan struct{})
		go func() {
			mgr.runInactivityPass(context.Background(), deps)
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatalf("la pasada %d no terminó al vencer su plazo", pass)
		}
	}
	if got := inactiveCalls.Load(); got != 2 {
		t.Errorf("FindInactiveSessions: esperaba 2 pasadas, got %d", got)
	}
	// Cada consulta tiene su propio plazo: la de activas colgada no deja sin pasada a las escaladas.
	if got := escalatedCalls.Load(); got != 2 {
		t.Errorf("FindEscalatedSessions: esperaba 2 pasadas, got %d", got)
	}
}

// slowInternalBird tarda en enviar el recordatorio al agente (Bird lento, sin ctx).
type slowInternalBird struct {
	*mockInactivityBird
	delay time.Duration
}

func (b *slowInternalBird) SendInternalText(phone, conv, text string) (string, error) {
	time.Sleep(b.delay)
	return b.mockInactivityBird.SendInternalText(phone, conv, text)
}

// D-07: una fila que cruza el plazo de la pasada termina igual (el recordatorio enviado queda con su
// marca, con un ctx vivo), la fila siguiente no arranca en esa pasada y sí en la siguiente.
func TestCheckEscalatedSessions_RowOutlivesPassDeadline(t *testing.T) {
	now := time.Now()
	waiting := func(id string) EscalatedSession {
		return EscalatedSession{
			ID: id, PhoneNumber: "+57300000000" + id, ConversationID: "conv-" + id,
			LastPatientMsg: now.Add(-20 * time.Minute), EscalatedAt: now.Add(-20 * time.Minute),
		}
	}
	marked := map[string]error{} // id → ctx.Err() del ctx con que se guardó la marca
	repo := &mockRepo{
		findEscalatedFn: func(context.Context) ([]EscalatedSession, error) {
			var out []EscalatedSession
			for _, id := range []string{"1", "2"} {
				if _, done := marked[id]; !done {
					out = append(out, waiting(id))
				}
			}
			return out, nil
		},
		incrementRemFn: func(ctx context.Context, id string) error {
			marked[id] = ctx.Err()
			return ctx.Err()
		},
	}
	mgr := NewSessionManager(repo, 120)
	mgr.passTimeout = 20 * time.Millisecond
	bird := &slowInternalBird{mockInactivityBird: &mockInactivityBird{}, delay: 60 * time.Millisecond}
	deps := escalationDeps(nil)
	deps.BirdClient = bird

	mgr.runInactivityPass(context.Background(), deps)
	if len(bird.internalTexts) != 1 {
		t.Fatalf("pasada 1: esperaba 1 recordatorio (la fila 2 no arranca), got %d", len(bird.internalTexts))
	}
	if err, ok := marked["1"]; !ok || err != nil {
		t.Fatalf("pasada 1: el recordatorio enviado debe quedar marcado con un ctx vivo (ok=%v, err=%v)", ok, err)
	}
	if _, ok := marked["2"]; ok {
		t.Fatal("pasada 1: la fila 2 no debía arrancar tras vencer la pasada")
	}

	bird.delay = 0
	mgr.runInactivityPass(context.Background(), deps)
	if err, ok := marked["2"]; !ok || err != nil || len(bird.internalTexts) != 2 {
		t.Fatalf("pasada 2: la fila 2 debía procesarse (ok=%v, err=%v, enviados=%d)", ok, err, len(bird.internalTexts))
	}
}

// ctxTracker registra el ctx.Err() con que se traza cada sesión.
type ctxTracker struct{ logged map[string]error }

func (c *ctxTracker) LogEvent(ctx context.Context, sessionID, _, _ string, _ map[string]interface{}) {
	c.logged[sessionID] = ctx.Err()
}

// D-07: el cierre por inactividad que cruza el plazo de la pasada completa su traza; la sesión
// siguiente queda para la pasada siguiente.
func TestCheckInactiveSessions_RowOutlivesPassDeadline(t *testing.T) {
	closed := map[string]bool{}
	repo := &mockRepo{
		findInactiveFn: func(context.Context, int) ([]InactiveSession, error) {
			var out []InactiveSession
			for _, id := range []string{"1", "2"} {
				if !closed[id] {
					out = append(out, InactiveSession{ID: id, LastActivity: time.Now().Add(-time.Hour), Reminders: 1})
				}
			}
			return out, nil
		},
		updateStatusFn: func(ctx context.Context, id, _ string) error {
			time.Sleep(60 * time.Millisecond)
			closed[id] = true
			return ctx.Err()
		},
	}
	mgr := NewSessionManager(repo, 120)
	mgr.passTimeout = 20 * time.Millisecond
	tracker := &ctxTracker{logged: map[string]error{}}
	deps := InactivityDeps{BirdClient: &mockInactivityBird{}, Tracker: tracker, ReminderMin: 5, CloseMin: 15}

	mgr.runInactivityPass(context.Background(), deps)
	if err, ok := tracker.logged["1"]; !ok || err != nil {
		t.Fatalf("pasada 1: el cierre de la fila 1 debía completarse con ctx vivo (ok=%v, err=%v)", ok, err)
	}
	if closed["2"] {
		t.Fatal("pasada 1: la fila 2 no debía arrancar tras vencer la pasada")
	}
	mgr.runInactivityPass(context.Background(), deps)
	if !closed["2"] {
		t.Fatal("pasada 2: la fila 2 debía procesarse")
	}
}

// D-07: una pasada vencida deja UNA alerta (la de RunPass), no además el error de la consulta
// interrumpida: el AlertHandler sube cada ERROR a Telegram.
func TestRunInactivityPass_ExpiredPassAlertsOnce(t *testing.T) {
	var logged bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, nil)))
	defer slog.SetDefault(prev)

	repo := &mockRepo{findInactiveFn: func(ctx context.Context, _ int) ([]InactiveSession, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	mgr := NewSessionManager(repo, 120)
	mgr.passTimeout = 20 * time.Millisecond
	mgr.runInactivityPass(context.Background(), InactivityDeps{ReminderMin: 5, CloseMin: 15})

	out := logged.String()
	if n := strings.Count(out, "level=ERROR"); n != 1 || !strings.Contains(out, "loop=inactive-sessions") {
		t.Fatalf("esperaba UN error, el de la pasada vencida; got %d: %q", n, out)
	}
}
