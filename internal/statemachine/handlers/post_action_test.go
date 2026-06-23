package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/session"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
)

func testSess(state string) *session.Session {
	return &session.Session{
		ID:           "sess-1",
		PhoneNumber:  "+573001234567",
		CurrentState: state,
		Status:       session.StatusActive,
		Context:      make(map[string]string),
		CreatedAt:    time.Now(),
		ExpiresAt:    time.Now().Add(2 * time.Hour),
	}
}

func textM(text string) bird.InboundMessage {
	return bird.InboundMessage{
		ID: "msg-1", Phone: "+573001234567",
		MessageType: "text", Text: text, ReceivedAt: time.Now(),
	}
}

func postbackM(payload string) bird.InboundMessage {
	return bird.InboundMessage{
		ID: "msg-pb", Phone: "+573001234567",
		MessageType: "text", IsPostback: true,
		PostbackPayload: payload, Text: payload,
		ReceivedAt: time.Now(),
	}
}

func TestPostActionMenu_VerCitas(t *testing.T) {
	m := sm.NewMachine()
	RegisterPostActionHandlers(m, nil)

	sess := testSess(sm.StatePostActionMenu)
	result, err := m.Process(context.Background(), sess, postbackM("ver_citas"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateFetchAppointments {
		t.Errorf("expected FETCH_APPOINTMENTS, got %s", result.NextState)
	}
}

func TestPostActionMenu_Agente(t *testing.T) {
	m := sm.NewMachine()
	RegisterPostActionHandlers(m, nil)

	sess := testSess(sm.StatePostActionMenu)
	result, err := m.Process(context.Background(), sess, textM("agente"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateEscalateToAgent {
		t.Errorf("expected ESCALATE_TO_AGENT, got %s", result.NextState)
	}
}

func TestPostActionMenu_AgenteUpperCase(t *testing.T) {
	m := sm.NewMachine()
	RegisterPostActionHandlers(m, nil)

	sess := testSess(sm.StatePostActionMenu)
	result, _ := m.Process(context.Background(), sess, textM("AGENTE"))
	if result.NextState != sm.StateEscalateToAgent {
		t.Errorf("expected ESCALATE_TO_AGENT for uppercase, got %s", result.NextState)
	}
}

func TestPostActionMenu_InvalidInput(t *testing.T) {
	m := sm.NewMachine()
	RegisterPostActionHandlers(m, nil)

	sess := testSess(sm.StatePostActionMenu)
	result, err := m.Process(context.Background(), sess, textM("hola"))
	if err != nil {
		t.Fatal(err)
	}
	// Should stay in same state with retry
	if result.NextState != sm.StatePostActionMenu {
		t.Errorf("expected POST_ACTION_MENU (retry), got %s", result.NextState)
	}
}

func TestPostActionMenu_Numeric(t *testing.T) {
	m := sm.NewMachine()
	RegisterPostActionHandlers(m, nil)

	sess := testSess(sm.StatePostActionMenu)
	// "1" = ver_citas, "2" = menu_principal, "3" = terminar_chat
	result, err := m.Process(context.Background(), sess, textM("1"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateFetchAppointments {
		t.Errorf("expected FETCH_APPOINTMENTS for numeric '1', got %s", result.NextState)
	}
}

func TestFarewell(t *testing.T) {
	m := sm.NewMachine()
	RegisterPostActionHandlers(m, nil)

	sess := testSess(sm.StateFarewell)
	result, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateTerminated {
		t.Errorf("expected TERMINATED, got %s", result.NextState)
	}
	if len(result.Messages) == 0 {
		t.Error("expected farewell text")
	}
}

func TestTerminated(t *testing.T) {
	m := sm.NewMachine()
	RegisterPostActionHandlers(m, nil)

	sess := testSess(sm.StateTerminated)
	result, err := m.Process(context.Background(), sess, textM(""))
	if err != nil {
		t.Fatal(err)
	}
	if sess.Status != session.StatusCompleted {
		t.Errorf("expected status completed, got %s", sess.Status)
	}
	if result.NextState != sm.StateTerminated {
		t.Errorf("expected TERMINATED, got %s", result.NextState)
	}
}
