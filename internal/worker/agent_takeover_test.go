package worker

import (
	"context"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/session"
)

// Cuando un agente humano responde manualmente una conversación cuya sesión del bot está ACTIVA
// (sin escalación previa), el bot debe pausarse (marcar la sesión escalada) para no interferir:
// sin la pausa, el bot seguía respondiendo a la vez que el agente.

func TestHandleAgentTakeover_ActiveSession_Escalates(t *testing.T) {
	sess := &session.Session{ID: "s-tk", PhoneNumber: "+573001234567", Status: session.StatusActive, CurrentState: "ASK_DOCUMENT"}
	sm := newMockSessionMgmt()
	sm.findForAgentCmdFn = func(_ context.Context, _ string) (*session.Session, error) { return sess, nil }
	escalated := false
	sm.escalateFn = func(_ context.Context, s *session.Session, _ string) error {
		escalated = true
		s.Status = session.StatusEscalated
		return nil
	}
	pool := NewMessageWorkerPool(1, 10)
	pool.SetDependencies(sm, &mockMessageSender{}, &mockMessageProcessor{})

	pool.HandleAgentTakeover("+573001234567")

	if !escalated {
		t.Fatal("una sesión activa con intervención de agente debe quedar escalada (bot pausado)")
	}
}

func TestHandleAgentTakeover_AlreadyEscalated_NoOp(t *testing.T) {
	sess := &session.Session{ID: "s-esc", PhoneNumber: "+573001234567", Status: session.StatusEscalated}
	sm := newMockSessionMgmt()
	sm.findForAgentCmdFn = func(_ context.Context, _ string) (*session.Session, error) { return sess, nil }
	called := false
	sm.escalateFn = func(_ context.Context, _ *session.Session, _ string) error {
		called = true
		return nil
	}
	pool := NewMessageWorkerPool(1, 10)
	pool.SetDependencies(sm, &mockMessageSender{}, &mockMessageProcessor{})

	pool.HandleAgentTakeover("+573001234567")

	if called {
		t.Fatal("una sesión ya escalada no debe re-escalarse")
	}
}

func TestHandleAgentTakeover_NoSession_NoOp(_ *testing.T) {
	sm := newMockSessionMgmt()
	sm.findForAgentCmdFn = func(_ context.Context, _ string) (*session.Session, error) { return nil, nil }
	pool := NewMessageWorkerPool(1, 10)
	pool.SetDependencies(sm, &mockMessageSender{}, &mockMessageProcessor{})

	pool.HandleAgentTakeover("+573001234567") // no debe entrar en pánico ni crear sesión
}

func TestHandleAgentTakeover_NilDeps_NoPanic(_ *testing.T) {
	pool := NewMessageWorkerPool(1, 10)
	pool.HandleAgentTakeover("+573001234567")
}
