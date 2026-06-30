package worker

import (
	"context"
	"strings"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/session"
	"github.com/neuro-bot/neuro-bot/internal/statemachine"
	"github.com/neuro-bot/neuro-bot/internal/tracking"
)

// escalatedPool arma un pool con una sesión ESCALADA lista para recibir comandos de agente.
func escalatedPool(t *testing.T) (*MessageWorkerPool, *mockSessionMgmt, *mockMessageSender, *session.Session) {
	t.Helper()
	sm := newMockSessionMgmt()
	sess := &session.Session{
		ID: "s1", PhoneNumber: "+573001234567", Status: session.StatusEscalated,
		ConversationID: "conv1", CurrentState: "ASK_DOCUMENT", Context: map[string]string{},
	}
	sm.findOrCreateFn = func(_ context.Context, _ string) (*session.Session, bool, error) {
		return sess, false, nil
	}
	sender := &mockMessageSender{}
	pool := NewMessageWorkerPool(1, 10)
	pool.SetDependencies(sm, sender, &mockMessageProcessor{})
	return pool, sm, sender, sess
}

func countSent(s *mockMessageSender, msgType string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, m := range s.sent {
		if m.msgType == msgType {
			n++
		}
	}
	return n
}

func TestAgentCommand_Close(t *testing.T) {
	pool, sm, sender, _ := escalatedPool(t)
	completeCalled := false
	sm.completeFn = func(_ context.Context, s *session.Session) error {
		completeCalled = true
		s.Status = session.StatusCompleted
		return nil
	}

	pool.processAgentCommand(context.Background(), AgentCommand{Action: "close", Phone: "+573001234567"})

	if !completeCalled {
		t.Error("expected Complete to be called on /bot cerrar")
	}
	if countSent(sender, "text") == 0 {
		t.Error("expected a goodbye text to the patient")
	}
}

// M4 (re-análisis): al cerrar por agente debe emitirse session_completed (además de escalation_closed),
// para que el StatCard 'Completadas' y avg_session_duration coincidan con el donut por status.
func TestAgentCommand_Close_EmitsSessionCompleted(t *testing.T) {
	pool, sm, _, _ := escalatedPool(t)
	sm.completeFn = func(_ context.Context, s *session.Session) error {
		s.Status = session.StatusCompleted
		return nil
	}
	store := &mockEventStoreWorker{}
	pool.SetTracker(tracking.NewEventTracker(store))

	pool.processAgentCommand(context.Background(), AgentCommand{Action: "close", Phone: "+573001234567"})

	store.mu.Lock()
	defer store.mu.Unlock()
	var hasCompleted, hasClosed bool
	for _, e := range store.insertedEvents {
		switch e.EventType {
		case "session_completed":
			hasCompleted = true
		case "escalation_closed":
			hasClosed = true
		}
	}
	if !hasCompleted {
		t.Error("handleAgentClose debe emitir session_completed")
	}
	if !hasClosed {
		t.Error("handleAgentClose debe seguir emitiendo escalation_closed")
	}
}

func TestAgentCommand_Reset(t *testing.T) {
	pool, sm, sender, _ := escalatedPool(t)
	var resumeTarget string
	sm.resumeFn = func(_ context.Context, s *session.Session, target string) error {
		resumeTarget = target
		s.Status = session.StatusActive
		s.CurrentState = target
		return nil
	}

	pool.processAgentCommand(context.Background(), AgentCommand{Action: "reset", Phone: "+573001234567"})

	if resumeTarget != statemachine.StateGreeting {
		t.Errorf("expected reset to resume at GREETING, got %q", resumeTarget)
	}
	if countSent(sender, "text") == 0 {
		t.Error("expected a text notifying the patient of the restart")
	}
}

func TestAgentCommand_ResumeWithData(t *testing.T) {
	pool, sm, sender, _ := escalatedPool(t)
	var resumeTarget string
	sm.resumeFn = func(_ context.Context, s *session.Session, target string) error {
		resumeTarget = target
		s.Status = session.StatusActive
		s.CurrentState = target
		return nil
	}
	processed := false
	pool.machine = &mockMessageProcessor{
		processFn: func(_ context.Context, _ *session.Session, _ bird.InboundMessage) (*statemachine.StateResult, error) {
			processed = true
			return &statemachine.StateResult{NextState: "MAIN_MENU"}, nil
		},
	}

	pool.processAgentCommand(context.Background(), AgentCommand{
		Action: "resume", State: "ASK_DOCUMENT", Data: "1234567890", Phone: "+573001234567",
	})

	if resumeTarget != "ASK_DOCUMENT" {
		t.Errorf("expected resume target ASK_DOCUMENT, got %q", resumeTarget)
	}
	if !processed {
		t.Error("expected the injected data to be processed by the state machine")
	}
	if countSent(sender, "text") == 0 {
		t.Error("expected a text to the patient on resume")
	}
}

func TestAgentCommand_Info(t *testing.T) {
	pool, _, sender, _ := escalatedPool(t)

	pool.processAgentCommand(context.Background(), AgentCommand{Action: "info", Phone: "+573001234567"})

	if countSent(sender, "internal") == 0 {
		t.Fatal("expected an internal (agent-only) info message")
	}
	sender.mu.Lock()
	defer sender.mu.Unlock()
	found := false
	for _, m := range sender.sent {
		if m.msgType == "internal" && strings.Contains(m.text, "Info de sesion") {
			found = true
		}
	}
	if !found {
		t.Error("expected the info message to contain the session summary")
	}
}

func TestAgentCommand_Cups(t *testing.T) {
	pool, sm, sender, sess := escalatedPool(t)
	var resumeTarget string
	sm.resumeFn = func(_ context.Context, s *session.Session, target string) error {
		resumeTarget = target
		s.Status = session.StatusActive
		s.CurrentState = target
		return nil
	}

	pool.processAgentCommand(context.Background(), AgentCommand{
		Action: "cups", Data: "883141:1 930810:2", Phone: "+573001234567",
	})

	if resumeTarget != statemachine.StateValidateOCR {
		t.Errorf("expected resume to VALIDATE_OCR, got %q", resumeTarget)
	}
	if !strings.Contains(sess.Context["ocr_cups_json"], "883141") {
		t.Errorf("expected injected CUPS in session context, got %q", sess.Context["ocr_cups_json"])
	}
	if countSent(sender, "internal") == 0 {
		t.Error("expected an internal summary of injected CUPS")
	}
}

func TestIsBotInterstitialMessage(t *testing.T) {
	botMsgs := []string{
		"Te voy a conectar con un agente. Un momento por favor...",
		"Hemos retomado tu atención. Continuamos con tu proceso.",
		"Tu consulta ha sido resuelta. Gracias por comunicarte con nosotros!",
		"Tu mensaje anterior está siendo procesado. Por favor espera un momento antes de enviar otro mensaje.",
	}
	for _, m := range botMsgs {
		if !IsBotInterstitialMessage(m) {
			t.Errorf("expected bot interstitial detected: %q", m)
		}
	}
	agentMsgs := []string{
		"Hola, soy Lorena del call center, ¿en qué le ayudo?",
		"Su cita quedó para el martes a las 10am",
		"",
	}
	for _, m := range agentMsgs {
		if IsBotInterstitialMessage(m) {
			t.Errorf("agent message wrongly flagged as bot: %q", m)
		}
	}
}

func TestAgentCommand_NotEscalated_NoOp(t *testing.T) {
	sm := newMockSessionMgmt()
	active := &session.Session{ID: "s2", PhoneNumber: "+573001234567", Status: session.StatusActive, Context: map[string]string{}}
	sm.findOrCreateFn = func(_ context.Context, _ string) (*session.Session, bool, error) {
		return active, false, nil
	}
	resumed := false
	sm.resumeFn = func(_ context.Context, _ *session.Session, _ string) error { resumed = true; return nil }
	sender := &mockMessageSender{}
	pool := NewMessageWorkerPool(1, 10)
	pool.SetDependencies(sm, sender, &mockMessageProcessor{})

	pool.processAgentCommand(context.Background(), AgentCommand{Action: "close", Phone: "+573001234567"})

	if resumed {
		t.Error("agent command on a non-escalated session must be a no-op")
	}
	if countSent(sender, "text") != 0 {
		t.Error("expected no messages for a non-escalated session")
	}
}
