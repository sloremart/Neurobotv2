package worker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/session"
)

// D-02: la fila del WAL de una resuelta o devuelta de Néctar la cierra el worker al terminar el
// comando; lo que no se encoló o no se procesó queda 'pending' para el replay.

const nectarTestKey = "nectar:77"

// nectarEscalatedPool es escalatedPool con la sesión de una conversación de Néctar y un WAL falso.
func nectarEscalatedPool(t *testing.T) (*MessageWorkerPool, *mockSessionMgmt, *fakeInboxMarker, *session.Session) {
	t.Helper()
	pool, sm, _, sess := escalatedPool(t)
	sess.PhoneNumber = nectarTestKey
	inbox := &fakeInboxMarker{}
	pool.SetInboxRepo(inbox)
	return pool, sm, inbox, sess
}

func TestEnqueueAgentCommand_FullBufferReportsNotEnqueued(t *testing.T) {
	pool := NewMessageWorkerPool(1, 10)
	for i := 0; i < agentCmdQueueSize; i++ {
		if !pool.EnqueueAgentCommand(AgentCommand{Action: "close", Phone: nectarTestKey}) {
			t.Fatalf("el comando %d cabe en el buffer", i)
		}
	}
	if pool.EnqueueAgentCommand(AgentCommand{Action: "close", Phone: nectarTestKey, EventID: "e-full"}) {
		t.Fatal("con el buffer lleno debe informar que no encoló")
	}
}

// Con el pool ya parado nada entra al buffer: quien trajo el comando de una fila del WAL la deja
// 'pending' (lo que quedó en el buffer al apagar lo cubre el test del manejador en internal/api).
func TestEnqueueAgentCommand_AfterStopReportsNotEnqueued(t *testing.T) {
	pool := NewMessageWorkerPool(1, 10)
	pool.Stop()
	if pool.EnqueueAgentCommand(AgentCommand{Action: "close", Phone: nectarTestKey, EventID: "e-3"}) {
		t.Fatal("con el pool cerrando no debe encolar")
	}
}

func TestEnqueueAgentCommand_AppContextDoneReportsNotEnqueued(t *testing.T) {
	pool := NewMessageWorkerPool(1, 10)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	pool.ctx = ctx // lo que deja Start; sin arrancar workers para que nada tome el comando
	if pool.EnqueueAgentCommand(AgentCommand{Action: "close", Phone: nectarTestKey, EventID: "e-off"}) {
		t.Fatal("con el bot apagándose no debe encolar")
	}
}

func TestProcessAgentCommand_ClosesItsWALRowOnce(t *testing.T) {
	for _, action := range []string{"close", "resume"} {
		t.Run(action, func(t *testing.T) {
			pool, _, inbox, _ := nectarEscalatedPool(t)
			pool.processAgentCommand(context.Background(),
				AgentCommand{Action: action, Phone: nectarTestKey, EventID: "e-" + action})
			if len(inbox.done) != 1 || inbox.done[0] != "e-"+action {
				t.Fatalf("exactamente un MarkDone de su fila, got %v", inbox.done)
			}
		})
	}
}

func TestProcessAgentCommand_WithoutEventIDTouchesNoRow(t *testing.T) {
	pool, _, inbox, _ := nectarEscalatedPool(t)
	pool.processAgentCommand(context.Background(), AgentCommand{Action: "close", Phone: nectarTestKey})
	if n := inbox.doneCount(); n != 0 {
		t.Fatalf("un comando sin aviso (Bird, no_show) no cierra filas, got %v", inbox.done)
	}
}

// Ya no escalada: el comando no tiene nada que hacer, pero se procesó; su fila se cierra.
func TestProcessAgentCommand_NotEscalatedStillClosesRow(t *testing.T) {
	pool, _, inbox, sess := nectarEscalatedPool(t)
	sess.Status = session.StatusActive
	pool.processAgentCommand(context.Background(), AgentCommand{Action: "close", Phone: nectarTestKey, EventID: "e-x"})
	if len(inbox.done) != 1 {
		t.Fatalf("got %v", inbox.done)
	}
}

// Sin el candado del teléfono o sin poder leer la sesión el comando no se aplicó: su fila queda
// 'pending' y el replay de atascados lo vuelve a despachar.
func TestProcessAgentCommand_NotAppliedKeepsRowPending(t *testing.T) {
	t.Run("lock timeout", func(t *testing.T) {
		pool, sm, inbox, _ := nectarEscalatedPool(t)
		pool.lockTimeout = 50 * time.Millisecond
		if err := sm.mutex.Lock(context.Background(), nectarTestKey); err != nil {
			t.Fatal(err)
		}
		defer sm.mutex.Unlock(nectarTestKey)
		pool.processAgentCommand(context.Background(), AgentCommand{Action: "close", Phone: nectarTestKey, EventID: "e-lock"})
		if n := inbox.doneCount(); n != 0 {
			t.Fatalf("got %v", inbox.done)
		}
	})
	t.Run("session error", func(t *testing.T) {
		pool, sm, inbox, _ := nectarEscalatedPool(t)
		sm.findForAgentCmdFn = func(context.Context, string) (*session.Session, error) {
			return nil, errors.New("mysql down")
		}
		pool.processAgentCommand(context.Background(), AgentCommand{Action: "close", Phone: nectarTestKey, EventID: "e-db"})
		if n := inbox.doneCount(); n != 0 {
			t.Fatalf("got %v", inbox.done)
		}
	})
}

// Doble entrega del mismo aviso (el replay coincide con el worker): un solo efecto. El segundo
// comando encuentra la sesión ya fuera de escalación y no hace nada.
func TestAgentCommand_DuplicateNoticeHasSingleEffect(t *testing.T) {
	t.Run("resuelta", func(t *testing.T) {
		pool, sm, inbox, _ := nectarEscalatedPool(t)
		cmd := AgentCommand{Action: "close", Phone: nectarTestKey, EventID: "e-dup", Reason: CloseReasonResolved}
		runCommands(t, pool, inbox, cmd, cmd)
		if sm.completeCalls != 1 {
			t.Fatalf("un solo cierre, got %d", sm.completeCalls)
		}
	})
	t.Run("devuelta", func(t *testing.T) {
		pool, sm, inbox, _ := nectarEscalatedPool(t)
		resumes := 0
		sm.resumeFn = func(_ context.Context, s *session.Session, target string) error {
			resumes++
			s.Status = session.StatusActive
			s.CurrentState = target
			return nil
		}
		cmd := AgentCommand{Action: "resume", Phone: nectarTestKey, EventID: "e-dup"}
		runCommands(t, pool, inbox, cmd, cmd)
		if resumes != 1 {
			t.Fatalf("un solo retorno al bot, got %d", resumes)
		}
	})
}

// runCommands pasa los comandos por la cola de un pool arrancado y espera a que cierren sus filas.
func runCommands(t *testing.T, pool *MessageWorkerPool, inbox *fakeInboxMarker, cmds ...AgentCommand) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	pool.Start(ctx)
	for _, c := range cmds {
		if !pool.EnqueueAgentCommand(c) {
			t.Fatal("debe encolar")
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for inbox.doneCount() < len(cmds) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	pool.Stop()
	if n := inbox.doneCount(); n != len(cmds) {
		t.Fatalf("cada comando procesado cierra su fila: %d de %d", n, len(cmds))
	}
}

// Un aviso anterior a la escalación vigente es una copia tardía de uno ya aplicado: se ignora sin
// tocar la sesión y su fila se cierra. Uno posterior, o dentro de la holgura de reloj, se aplica.
func TestProcessAgentCommand_NoticeBeforeCurrentEscalation(t *testing.T) {
	escalatedAt := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	cases := []struct {
		name       string
		occurredAt time.Time
		wantApply  bool
	}{
		{"anterior a la escalación nueva se ignora", escalatedAt.Add(-10 * time.Minute), false},
		{"posterior a la escalación se aplica", escalatedAt.Add(5 * time.Minute), true},
		{"1 min antes, dentro de la holgura, se aplica", escalatedAt.Add(-time.Minute), true},
		{"sin occurred_at se aplica como siempre", time.Time{}, true},
	}
	for _, c := range cases {
		for _, action := range []string{"resume", "close"} {
			t.Run(c.name+"/"+action, func(t *testing.T) {
				pool, sm, inbox, sess := nectarEscalatedPool(t)
				sess.EscalatedAt = &escalatedAt
				resumes := 0
				sm.resumeFn = func(_ context.Context, s *session.Session, target string) error {
					resumes++
					s.Status = session.StatusActive
					s.CurrentState = target
					return nil
				}
				pool.processAgentCommand(context.Background(), AgentCommand{
					Action: action, Phone: nectarTestKey, EventID: "e-late", OccurredAt: c.occurredAt,
					Reason: map[string]string{"close": CloseReasonResolved}[action],
				})
				applied := resumes + sm.completeCalls
				if (applied == 1) != c.wantApply || applied > 1 {
					t.Fatalf("aplicado %d veces, quiero aplicar = %v", applied, c.wantApply)
				}
				if len(inbox.done) != 1 || inbox.done[0] != "e-late" {
					t.Fatalf("la fila se cierra en todo caso, got %v", inbox.done)
				}
				if !c.wantApply && sess.Status != session.StatusEscalated {
					t.Fatalf("la escalación vigente no se toca, status %q", sess.Status)
				}
			})
		}
	}
}

// Sin escalated_at en la sesión no hay con qué comparar: el aviso se aplica como siempre.
func TestProcessAgentCommand_NoticeWithoutEscalatedAtApplies(t *testing.T) {
	pool, sm, _, _ := nectarEscalatedPool(t)
	pool.processAgentCommand(context.Background(), AgentCommand{
		Action: "close", Phone: nectarTestKey, EventID: "e-old", Reason: CloseReasonResolved,
		OccurredAt: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	if sm.completeCalls != 1 {
		t.Fatalf("got %d", sm.completeCalls)
	}
}
