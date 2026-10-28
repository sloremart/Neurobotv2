package statemachine

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/session"
)

func newTestSession(state string) *session.Session {
	return &session.Session{
		ID:           "test-session-1",
		PhoneNumber:  "+573001234567",
		CurrentState: state,
		Status:       session.StatusActive,
		Context:      make(map[string]string),
		CreatedAt:    time.Now(),
		ExpiresAt:    time.Now().Add(2 * time.Hour),
	}
}

func newTestMessage(text string) bird.InboundMessage {
	return bird.InboundMessage{
		ID:          "msg-1",
		Phone:       "+573001234567",
		MessageType: "text",
		Text:        text,
		ReceivedAt:  time.Now(),
	}
}

func TestStateMachine_BasicHandlerExecution(t *testing.T) {
	m := NewMachine()

	// Use MAIN_MENU (interactive) as target so auto-chaining stops
	m.Register(StateMainMenu, func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*StateResult, error) {
		return NewResult(StateMainMenu).
			WithText("Hello!").
			WithEvent("test_event", nil), nil
	})

	sess := newTestSession(StateMainMenu)
	msg := newTestMessage("hello")

	result, err := m.Process(context.Background(), sess, msg)
	if err != nil {
		t.Fatal(err)
	}

	if result.NextState != StateMainMenu {
		t.Errorf("expected MAIN_MENU, got %s", result.NextState)
	}
	if len(result.Messages) != 1 {
		t.Errorf("expected 1 message, got %d", len(result.Messages))
	}
	if len(result.Events) != 1 {
		t.Errorf("expected 1 event, got %d", len(result.Events))
	}
}

func TestStateMachine_InterceptorBlocks(t *testing.T) {
	m := NewMachine()

	// Register a handler that should NOT be reached (use real interactive state)
	m.Register(StateAskDocument, func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*StateResult, error) {
		t.Fatal("handler should not be called when interceptor blocks")
		return nil, nil
	})

	// Register interceptor that intercepts "blocked" messages
	m.AddInterceptor(func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*StateResult, bool) {
		if msg.Text == "blocked" {
			return NewResult(StateMainMenu).WithText("Blocked by interceptor"), true
		}
		return nil, false
	})

	sess := newTestSession(StateAskDocument)

	// Blocked message
	result, err := m.Process(context.Background(), sess, newTestMessage("blocked"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != StateMainMenu {
		t.Errorf("expected MAIN_MENU, got %s", result.NextState)
	}

	// Non-blocked message goes through to handler
	m.Register(StateAskDocument, func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*StateResult, error) {
		return NewResult(StateConfirmIdentity).WithText("Handled normally"), nil
	})

	result2, err := m.Process(context.Background(), sess, newTestMessage("normal"))
	if err != nil {
		t.Fatal(err)
	}
	if result2.NextState != StateConfirmIdentity {
		t.Errorf("expected CONFIRM_IDENTITY, got %s", result2.NextState)
	}
}

func TestStateMachine_AutoChaining(t *testing.T) {
	m := NewMachine()

	// Register automatic state → chains to interactive state
	// Simulate: CHECK_BUSINESS_HOURS (auto) → GREETING (auto) → MAIN_MENU (interactive, stops)
	m.Register(StateCheckBusinessHours, func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*StateResult, error) {
		return NewResult(StateGreeting).
			WithEvent("session_started", nil), nil
	})

	m.Register(StateGreeting, func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*StateResult, error) {
		return NewResult(StateMainMenu).
			WithText("Bienvenido! ¿Qué deseas hacer?").
			WithEvent("greeting_sent", nil), nil
	})

	// MAIN_MENU is interactive → stops auto-chaining
	sess := newTestSession(StateCheckBusinessHours)
	result, err := m.Process(context.Background(), sess, newTestMessage("hi"))
	if err != nil {
		t.Fatal(err)
	}

	if result.NextState != StateMainMenu {
		t.Errorf("expected MAIN_MENU after auto-chaining, got %s", result.NextState)
	}

	// Should have accumulated messages from the chain
	if len(result.Messages) == 0 {
		t.Error("expected at least 1 message from auto-chain")
	}

	// Should have events from both auto states
	if len(result.Events) < 2 {
		t.Errorf("expected at least 2 events from auto-chain, got %d", len(result.Events))
	}
}

func TestStateMachine_UnregisteredState(t *testing.T) {
	m := NewMachine()
	sess := newTestSession("NONEXISTENT_STATE")

	_, err := m.Process(context.Background(), sess, newTestMessage("hello"))
	if err == nil {
		t.Error("expected error for unregistered state")
	}
}

func TestStateMachine_ContextPreservation(t *testing.T) {
	m := NewMachine()

	// Use real interactive states to avoid auto-chaining issues
	m.Register(StateMainMenu, func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*StateResult, error) {
		return NewResult(StateAskDocument).
			WithContext("key1", "value1").
			WithContext("key2", "value2"), nil
	})

	sess := newTestSession(StateMainMenu)
	result, err := m.Process(context.Background(), sess, newTestMessage("test"))
	if err != nil {
		t.Fatal(err)
	}

	if result.UpdateCtx["key1"] != "value1" {
		t.Errorf("expected key1=value1, got %s", result.UpdateCtx["key1"])
	}
	if result.UpdateCtx["key2"] != "value2" {
		t.Errorf("expected key2=value2, got %s", result.UpdateCtx["key2"])
	}
}

func TestStateMachine_ClearCtxMergeInAutoChain(t *testing.T) {
	m := NewMachine()

	// First auto handler sets ClearCtx
	m.Register(StateCheckBusinessHours, func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*StateResult, error) {
		return NewResult(StateGreeting).WithClearCtx("key_a"), nil
	})

	// Second auto handler sets different ClearCtx
	m.Register(StateGreeting, func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*StateResult, error) {
		return NewResult(StateMainMenu).WithClearCtx("key_b"), nil
	})

	sess := newTestSession(StateCheckBusinessHours)
	result, err := m.Process(context.Background(), sess, newTestMessage("hi"))
	if err != nil {
		t.Fatal(err)
	}

	// Both ClearCtx should be merged
	if len(result.ClearCtx) < 2 {
		t.Errorf("expected at least 2 ClearCtx entries, got %d: %v", len(result.ClearCtx), result.ClearCtx)
	}
}

func TestStateMachine_ContextMergePrecedence(t *testing.T) {
	m := NewMachine()

	// First handler (auto) sets key=prev
	m.Register(StateCheckBusinessHours, func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*StateResult, error) {
		return NewResult(StateGreeting).WithContext("shared", "from_first"), nil
	})

	// Second handler (auto) sets same key=auto — auto takes precedence
	m.Register(StateGreeting, func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*StateResult, error) {
		return NewResult(StateMainMenu).WithContext("shared", "from_second"), nil
	})

	sess := newTestSession(StateCheckBusinessHours)
	result, err := m.Process(context.Background(), sess, newTestMessage("hi"))
	if err != nil {
		t.Fatal(err)
	}

	// Auto handler's value should take precedence (auto > prev)
	if result.UpdateCtx["shared"] != "from_second" {
		t.Errorf("expected 'from_second' (auto handler precedence), got %q", result.UpdateCtx["shared"])
	}
}

func TestStateMachine_EventsAccumulateAcrossChain(t *testing.T) {
	m := NewMachine()

	m.Register(StateCheckBusinessHours, func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*StateResult, error) {
		return NewResult(StateGreeting).
			WithEvent("ev1", nil).
			WithEvent("ev2", nil), nil
	})

	m.Register(StateGreeting, func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*StateResult, error) {
		return NewResult(StateMainMenu).
			WithEvent("ev3", nil), nil
	})

	sess := newTestSession(StateCheckBusinessHours)
	result, err := m.Process(context.Background(), sess, newTestMessage("hi"))
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Events) != 3 {
		t.Errorf("expected 3 events accumulated, got %d", len(result.Events))
	}
}

func TestStateMachine_ErrorInAutoHandler(t *testing.T) {
	m := NewMachine()

	m.Register(StateCheckBusinessHours, func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*StateResult, error) {
		return NewResult(StateGreeting), nil
	})

	m.Register(StateGreeting, func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*StateResult, error) {
		return nil, fmt.Errorf("boom")
	})

	sess := newTestSession(StateCheckBusinessHours)
	_, err := m.Process(context.Background(), sess, newTestMessage("hi"))
	if err == nil {
		t.Fatal("expected error from auto handler")
	}
	if !strings.Contains(err.Error(), "auto handler") {
		t.Errorf("expected 'auto handler' in error, got: %s", err.Error())
	}
}

func TestStateMachine_AutoStateNoHandler_Stops(t *testing.T) {
	m := NewMachine()

	// Handler returns auto state (GREETING) with no handler registered for it
	m.Register(StateMainMenu, func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*StateResult, error) {
		return NewResult(StateGreeting).WithText("going to greeting"), nil
	})
	// StateGreeting is auto but NO handler registered → should break out of loop

	sess := newTestSession(StateMainMenu)
	result, err := m.Process(context.Background(), sess, newTestMessage("hi"))
	if err != nil {
		t.Fatal(err)
	}

	// Should stop at GREETING since no handler found
	if result.NextState != StateGreeting {
		t.Errorf("expected GREETING (no handler, stops), got %s", result.NextState)
	}
}

func TestStateMachine_SelfLoopStops(t *testing.T) {
	m := NewMachine()

	// An auto state that returns itself → should NOT infinite loop
	m.Register(StateCheckBusinessHours, func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*StateResult, error) {
		return NewResult(StateCheckBusinessHours).WithText("self loop"), nil
	})

	sess := newTestSession(StateCheckBusinessHours)
	result, err := m.Process(context.Background(), sess, newTestMessage("hi"))
	if err != nil {
		t.Fatal(err)
	}
	// Self-loop condition: result.NextState != sess.CurrentState is false → stops
	if result.NextState != StateCheckBusinessHours {
		t.Errorf("expected CHECK_BUSINESS_HOURS (self-loop stops), got %s", result.NextState)
	}
}

func TestStateMachine_MultipleInterceptorsOrder(t *testing.T) {
	m := NewMachine()

	// First interceptor intercepts "first"
	m.AddInterceptor(func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*StateResult, bool) {
		if msg.Text == "first" || msg.Text == "both" {
			return NewResult(StateMainMenu).WithText("interceptor_1"), true
		}
		return nil, false
	})

	// Second interceptor intercepts "second" and "both"
	m.AddInterceptor(func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*StateResult, bool) {
		if msg.Text == "second" || msg.Text == "both" {
			return NewResult(StateMainMenu).WithText("interceptor_2"), true
		}
		return nil, false
	})

	m.Register(StateAskDocument, func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*StateResult, error) {
		return NewResult(StateAskDocument).WithText("handler"), nil
	})

	sess := newTestSession(StateAskDocument)

	// "both" should be caught by first interceptor (order matters)
	result, _ := m.Process(context.Background(), sess, newTestMessage("both"))
	if len(result.Messages) == 0 {
		t.Fatal("expected message")
	}
	if txt, ok := result.Messages[0].(*TextMessage); !ok || txt.Text != "interceptor_1" {
		t.Error("expected first interceptor to win")
	}
}

func TestStateMachine_RetryCountResetOnStateTransition(t *testing.T) {
	m := NewMachine()

	// Handler transitions from MAIN_MENU to ASK_DOCUMENT (different state)
	m.Register(StateMainMenu, func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*StateResult, error) {
		return NewResult(StateAskDocument).WithText("next"), nil
	})

	sess := newTestSession(StateMainMenu)
	sess.RetryCount = 5 // Simulate retries in current state

	result, err := m.Process(context.Background(), sess, newTestMessage("go"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != StateAskDocument {
		t.Errorf("expected ASK_DOCUMENT, got %s", result.NextState)
	}
	// Process resets RetryCount when state changes (line ~65-66 in machine.go)
	if sess.RetryCount != 0 {
		t.Errorf("expected RetryCount=0 after state transition, got %d", sess.RetryCount)
	}
}

func TestStateMachine_RetryCountPreservedOnSameState(t *testing.T) {
	m := NewMachine()

	// Handler stays on same interactive state (e.g., invalid input retry)
	m.Register(StateMainMenu, func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*StateResult, error) {
		return NewResult(StateMainMenu).WithText("try again"), nil
	})

	sess := newTestSession(StateMainMenu)
	sess.RetryCount = 3

	result, err := m.Process(context.Background(), sess, newTestMessage("bad"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != StateMainMenu {
		t.Errorf("expected MAIN_MENU, got %s", result.NextState)
	}
	// Same state → RetryCount should NOT be reset
	if sess.RetryCount != 3 {
		t.Errorf("expected RetryCount=3 (preserved), got %d", sess.RetryCount)
	}
}

func TestStateMachine_AutoChainCycleGuard(t *testing.T) {
	m := NewMachine()

	// We need 25+ distinct auto states to test the maxAutoChain=20 guard.
	// Use existing auto states and register them in a long chain.
	// Chain: CHECK_BUSINESS_HOURS → GREETING → PATIENT_LOOKUP → OUT_OF_HOURS →
	//        FETCH_APPOINTMENTS → SHOW_CONTACT_INFO → CREATE_PATIENT →
	//        APPOINTMENT_CONFIRMED → APPOINTMENT_CANCELLED → VALIDATE_OCR →
	//        OCR_FAILED → CHECK_EXISTING → APPOINTMENT_EXISTS → ASK_CONTRASTED →
	//        ASK_PREGNANCY → PREGNANCY_BLOCK → GFR_RESULT → GFR_NOT_ELIGIBLE →
	//        ASK_SEDATION → CHECK_PRIOR_CONSULTATION → CHECK_SOAT_LIMIT →
	//        CHECK_AGE_RESTRICTION → SEARCH_SLOTS → NO_SLOTS_AVAILABLE →
	//        CREATE_APPOINTMENT
	autoStates := []string{
		StateCheckBusinessHours,   // 0
		StateGreeting,             // 1
		StatePatientLookup,        // 2
		StateOutOfHours,           // 3
		StateFetchAppointments,    // 4
		StateShowContactInfo,      // 5  (NO_APPOINTMENTS is now Interactive)
		StateCreatePatient,        // 6
		StateAppointmentConfirmed, // 7
		StateAppointmentCancelled, // 8
		StateValidateOCR,          // 9
		StateOCRFailed,            // 10
		StateCheckExisting,        // 11
		StateAppointmentExists,    // 12
		StateAskContrasted,        // 13
		StateAskPregnancy,         // 14
		StatePregnancyBlock,       // 15
		StateGfrResult,            // 16
		StateGfrNotEligible,       // 17
		StateAskSedation,          // 18
		StateCheckPriorConsult,    // 19
		StateCheckMRCLimit,        // 20
		StateCheckAgeRestriction,  // 21
		StateSearchSlots,          // 22
		StateNoSlotsAvailable,     // 23
		StateCreateAppointment,    // 24
	}

	// Register chain: each auto state points to the next one
	for i := 0; i < len(autoStates)-1; i++ {
		next := autoStates[i+1]
		m.Register(autoStates[i], func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*StateResult, error) {
			return NewResult(next).WithText("step"), nil
		})
	}
	// Last one also points to another auto state (but won't be reached)
	m.Register(autoStates[len(autoStates)-1], func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*StateResult, error) {
		return NewResult(StateBookingSuccess).WithText("end"), nil
	})

	sess := newTestSession(StateCheckBusinessHours)
	result, err := m.Process(context.Background(), sess, newTestMessage("hi"))
	if err != nil {
		t.Fatal(err)
	}

	// maxAutoChain = 20, so the loop should stop at iteration 20.
	// The initial handler runs (step 0), then the loop runs iterations 0..19.
	// At iteration 20, the guard triggers and breaks.
	// Result state should be the state at the 21st position (index 20) in the chain.
	// Initial handler: CHECK_BUSINESS_HOURS → GREETING (iteration 0 enters)
	// Iteration 0: GREETING → PATIENT_LOOKUP
	// ...iteration 19: autoStates[20] = CHECK_SOAT_LIMIT → autoStates[21] = CHECK_AGE_RESTRICTION
	// Iteration 20: i >= 20 → break. Result is CHECK_AGE_RESTRICTION.
	// But we also need to check it didn't panic or infinite loop.
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	// Verify we got messages from the chain (at least several)
	if len(result.Messages) < 20 {
		t.Errorf("expected at least 20 messages from chain, got %d", len(result.Messages))
	}
}

func TestStateMachine_AutoChainVisitedCycleDetection(t *testing.T) {
	m := NewMachine()

	// Create a cycle: CHECK_BUSINESS_HOURS → GREETING → CHECK_BUSINESS_HOURS
	// The visited map should detect this and break.
	callCount := 0
	m.Register(StateCheckBusinessHours, func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*StateResult, error) {
		callCount++
		return NewResult(StateGreeting).WithText("to greeting"), nil
	})

	m.Register(StateGreeting, func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*StateResult, error) {
		return NewResult(StateCheckBusinessHours).WithText("back to check"), nil
	})

	sess := newTestSession(StateCheckBusinessHours)
	result, err := m.Process(context.Background(), sess, newTestMessage("hi"))
	if err != nil {
		t.Fatal(err)
	}

	// Trace:
	// 1. Initial handler (CHECK_BUSINESS_HOURS) → NextState=GREETING, msg="to greeting"
	// 2. Loop iter 0: visited[GREETING]=true, sess=GREETING, GREETING handler → NextState=CHECK_BUSINESS_HOURS, msg="back to check"
	// 3. Loop iter 1: visited[CHECK_BUSINESS_HOURS]=true, sess=CHECK_BUSINESS_HOURS, handler → NextState=GREETING, msg="to greeting"
	// 4. Loop iter 2: visited[GREETING] already true → cycle detected, break
	// Result NextState = GREETING (from step 3)
	if result.NextState != StateGreeting {
		t.Errorf("expected GREETING (cycle break), got %s", result.NextState)
	}

	// Messages: 3 total (initial + 2 loop iterations)
	if len(result.Messages) != 3 {
		t.Errorf("expected 3 messages (initial + 2 loop iterations), got %d", len(result.Messages))
	}
}

func TestStateMachine_ThreeLevelAutoChain(t *testing.T) {
	m := NewMachine()

	// Three auto states chaining to one interactive state
	// CHECK_BUSINESS_HOURS → GREETING → PATIENT_LOOKUP → MAIN_MENU (interactive, stops)
	m.Register(StateCheckBusinessHours, func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*StateResult, error) {
		return NewResult(StateGreeting).
			WithText("msg1").
			WithEvent("ev_check", nil), nil
	})

	m.Register(StateGreeting, func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*StateResult, error) {
		return NewResult(StatePatientLookup).
			WithText("msg2").
			WithEvent("ev_greeting", nil), nil
	})

	m.Register(StatePatientLookup, func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*StateResult, error) {
		return NewResult(StateMainMenu).
			WithText("msg3").
			WithEvent("ev_lookup", nil), nil
	})

	sess := newTestSession(StateCheckBusinessHours)
	result, err := m.Process(context.Background(), sess, newTestMessage("hi"))
	if err != nil {
		t.Fatal(err)
	}

	// Should land on interactive MAIN_MENU
	if result.NextState != StateMainMenu {
		t.Errorf("expected MAIN_MENU, got %s", result.NextState)
	}

	// All 3 auto handlers' messages should be accumulated
	if len(result.Messages) != 3 {
		t.Errorf("expected 3 messages, got %d", len(result.Messages))
	}
	for i, expected := range []string{"msg1", "msg2", "msg3"} {
		txt, ok := result.Messages[i].(*TextMessage)
		if !ok {
			t.Errorf("message %d: expected TextMessage", i)
			continue
		}
		if txt.Text != expected {
			t.Errorf("message %d: expected %q, got %q", i, expected, txt.Text)
		}
	}

	// All 3 events should be accumulated
	if len(result.Events) != 3 {
		t.Errorf("expected 3 events, got %d", len(result.Events))
	}
	for i, expectedType := range []string{"ev_check", "ev_greeting", "ev_lookup"} {
		if result.Events[i].Type != expectedType {
			t.Errorf("event %d: expected %q, got %q", i, expectedType, result.Events[i].Type)
		}
	}
}

func TestStateMachine_InterceptorDoesNotBlockAutoChainTarget(t *testing.T) {
	m := NewMachine()

	interceptorCalls := 0
	// Register interceptor that intercepts any message containing "hi"
	m.AddInterceptor(func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*StateResult, bool) {
		interceptorCalls++
		// Only intercept for the initial call — interceptor runs once before handler
		if msg.Text == "hi" && sess.CurrentState == StateAskDocument {
			return NewResult(StateMainMenu).WithText("intercepted"), true
		}
		return nil, false
	})

	// Start from MAIN_MENU (interactive), handler returns GREETING (auto)
	// GREETING handler returns ASK_DOCUMENT (interactive) → stops auto-chain
	// The interceptor should NOT run again during auto-chaining;
	// it only runs once at the beginning of Process.
	m.Register(StateMainMenu, func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*StateResult, error) {
		return NewResult(StateGreeting).WithText("going to greeting"), nil
	})

	m.Register(StateGreeting, func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*StateResult, error) {
		return NewResult(StateAskDocument).WithText("ask doc"), nil
	})

	sess := newTestSession(StateMainMenu)
	result, err := m.Process(context.Background(), sess, newTestMessage("hi"))
	if err != nil {
		t.Fatal(err)
	}

	// Interceptor was called once at the start (for MAIN_MENU state) and did not intercept
	// The auto-chain should have completed without the interceptor running again
	if result.NextState != StateAskDocument {
		t.Errorf("expected ASK_DOCUMENT (auto-chain completed), got %s", result.NextState)
	}

	// Interceptor ran exactly once (initial check before the handler)
	if interceptorCalls != 1 {
		t.Errorf("expected interceptor called 1 time, got %d", interceptorCalls)
	}

	// Verify messages accumulated from both handlers
	if len(result.Messages) != 2 {
		t.Errorf("expected 2 messages, got %d", len(result.Messages))
	}
}

// === EscalationKeywordsInterceptor tests ===

func newPostbackMessage(payload string) bird.InboundMessage {
	return bird.InboundMessage{
		ID:              "msg-pb-1",
		Phone:           "+573001234567",
		MessageType:     "postback",
		Text:            payload,
		IsPostback:      true,
		PostbackPayload: payload,
		ReceivedAt:      time.Now(),
	}
}

func TestEscalationInterceptor_Keyword(t *testing.T) {
	interceptor := EscalationKeywordsInterceptor()
	sess := newTestSession(StateAskDocument)
	sess.RetryCount = 2
	msg := newTestMessage("agente")

	result, handled := interceptor(context.Background(), sess, msg)
	if !handled {
		t.Fatal("expected handled")
	}
	if result.NextState != StateEscalateToAgent {
		t.Errorf("expected ESCALATE_TO_AGENT, got %s", result.NextState)
	}
	if sess.RetryCount != 0 {
		t.Errorf("expected retry count reset, got %d", sess.RetryCount)
	}
	// Verify event was emitted
	if len(result.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(result.Events))
	}
	if result.Events[0].Type != "escalation_requested" {
		t.Errorf("expected event type 'escalation_requested', got %q", result.Events[0].Type)
	}
}

func TestEscalationInterceptor_SkipTerminated(t *testing.T) {
	interceptor := EscalationKeywordsInterceptor()
	sess := newTestSession(StateTerminated)
	msg := newTestMessage("agente")

	_, handled := interceptor(context.Background(), sess, msg)
	if handled {
		t.Fatal("expected not handled for TERMINATED state")
	}
}

func TestEscalationInterceptor_SkipEscalated(t *testing.T) {
	interceptor := EscalationKeywordsInterceptor()
	sess := newTestSession(StateEscalated)
	msg := newTestMessage("agente")

	_, handled := interceptor(context.Background(), sess, msg)
	if handled {
		t.Fatal("expected not handled for ESCALATED state")
	}
}

func TestEscalationInterceptor_SkipEscalateToAgent(t *testing.T) {
	interceptor := EscalationKeywordsInterceptor()
	sess := newTestSession(StateEscalateToAgent)
	msg := newTestMessage("humano")

	_, handled := interceptor(context.Background(), sess, msg)
	if handled {
		t.Fatal("expected not handled for ESCALATE_TO_AGENT state")
	}
}

func TestEscalationInterceptor_SkipPostback(t *testing.T) {
	interceptor := EscalationKeywordsInterceptor()
	sess := newTestSession(StateAskDocument)
	msg := newPostbackMessage("agente")

	_, handled := interceptor(context.Background(), sess, msg)
	if handled {
		t.Fatal("expected not handled for postback messages")
	}
}

func TestEscalationInterceptor_NonKeyword(t *testing.T) {
	interceptor := EscalationKeywordsInterceptor()
	sess := newTestSession(StateAskDocument)
	msg := newTestMessage("hello")

	_, handled := interceptor(context.Background(), sess, msg)
	if handled {
		t.Fatal("expected not handled for non-keyword input")
	}
}

func TestEscalationInterceptor_AllKeywords(t *testing.T) {
	keywords := []string{"agente", "asesor", "humano", "ayuda"}
	interceptor := EscalationKeywordsInterceptor()

	for _, kw := range keywords {
		t.Run(kw, func(t *testing.T) {
			sess := newTestSession(StateMainMenu)
			msg := newTestMessage(kw)

			result, handled := interceptor(context.Background(), sess, msg)
			if !handled {
				t.Fatalf("expected keyword %q to be handled", kw)
			}
			if result.NextState != StateEscalateToAgent {
				t.Errorf("expected ESCALATE_TO_AGENT for keyword %q, got %s", kw, result.NextState)
			}
		})
	}
}

func TestEscalationInterceptor_CaseInsensitive(t *testing.T) {
	interceptor := EscalationKeywordsInterceptor()
	sess := newTestSession(StateAskDocument)
	msg := newTestMessage("  AGENTE  ")

	result, handled := interceptor(context.Background(), sess, msg)
	if !handled {
		t.Fatal("expected handled for uppercase keyword with spaces")
	}
	if result.NextState != StateEscalateToAgent {
		t.Errorf("expected ESCALATE_TO_AGENT, got %s", result.NextState)
	}
}

// === Message Type() tests ===

func TestMessageTypes(t *testing.T) {
	txt := &TextMessage{Text: "hello"}
	if txt.Type() != "text" {
		t.Errorf("expected 'text', got %q", txt.Type())
	}

	btn := &ButtonMessage{Text: "pick", Buttons: []Button{{Text: "A", Payload: "a"}}}
	if btn.Type() != "interactive_buttons" {
		t.Errorf("expected 'interactive_buttons', got %q", btn.Type())
	}

	list := &ListMessage{Body: "b", Title: "t", Sections: nil}
	if list.Type() != "interactive_list" {
		t.Errorf("expected 'interactive_list', got %q", list.Type())
	}
}

// === Result builder method tests ===

func TestResultWithButtons(t *testing.T) {
	r := NewResult("X").WithButtons("text", Button{Text: "A", Payload: "a"}, Button{Text: "B", Payload: "b"})
	if len(r.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(r.Messages))
	}
	bm, ok := r.Messages[0].(*ButtonMessage)
	if !ok {
		t.Fatal("expected *ButtonMessage")
	}
	if bm.Text != "text" {
		t.Errorf("expected button text 'text', got %q", bm.Text)
	}
	if len(bm.Buttons) != 2 {
		t.Errorf("expected 2 buttons, got %d", len(bm.Buttons))
	}
	if bm.Buttons[0].Text != "A" || bm.Buttons[0].Payload != "a" {
		t.Errorf("button 0 mismatch: %+v", bm.Buttons[0])
	}
	if bm.Buttons[1].Text != "B" || bm.Buttons[1].Payload != "b" {
		t.Errorf("button 1 mismatch: %+v", bm.Buttons[1])
	}
}

func TestResultWithList(t *testing.T) {
	sections := []ListSection{
		{Title: "Section1", Rows: []ListRow{{ID: "1", Title: "Row1", Description: "Desc1"}}},
	}
	r := NewResult("Y").WithList("body text", "list title", sections...)
	if len(r.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(r.Messages))
	}
	lm, ok := r.Messages[0].(*ListMessage)
	if !ok {
		t.Fatal("expected *ListMessage")
	}
	if lm.Body != "body text" {
		t.Errorf("expected body 'body text', got %q", lm.Body)
	}
	if lm.Title != "list title" {
		t.Errorf("expected title 'list title', got %q", lm.Title)
	}
	if len(lm.Sections) != 1 {
		t.Fatalf("expected 1 section, got %d", len(lm.Sections))
	}
	if lm.Sections[0].Title != "Section1" {
		t.Errorf("expected section title 'Section1', got %q", lm.Sections[0].Title)
	}
	if len(lm.Sections[0].Rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(lm.Sections[0].Rows))
	}
}

func TestResultWithContextMap(t *testing.T) {
	r := NewResult("Z").
		WithContext("existing", "val0").
		WithContextMap(map[string]string{
			"key1": "val1",
			"key2": "val2",
		})

	if len(r.UpdateCtx) != 3 {
		t.Fatalf("expected 3 context entries, got %d", len(r.UpdateCtx))
	}
	if r.UpdateCtx["existing"] != "val0" {
		t.Errorf("expected 'val0' for 'existing', got %q", r.UpdateCtx["existing"])
	}
	if r.UpdateCtx["key1"] != "val1" {
		t.Errorf("expected 'val1' for 'key1', got %q", r.UpdateCtx["key1"])
	}
	if r.UpdateCtx["key2"] != "val2" {
		t.Errorf("expected 'val2' for 'key2', got %q", r.UpdateCtx["key2"])
	}
}

func TestResultWithContextMap_NilInit(t *testing.T) {
	// Test WithContextMap when UpdateCtx is nil (no prior WithContext call)
	r := NewResult("W").WithContextMap(map[string]string{"a": "1"})
	if r.UpdateCtx["a"] != "1" {
		t.Errorf("expected '1' for 'a', got %q", r.UpdateCtx["a"])
	}
}

func TestResultWithContextMap_OverwriteExisting(t *testing.T) {
	r := NewResult("V").
		WithContext("key", "old").
		WithContextMap(map[string]string{"key": "new"})

	if r.UpdateCtx["key"] != "new" {
		t.Errorf("expected 'new' (overwritten), got %q", r.UpdateCtx["key"])
	}
}

// === IsAutomatic tests for edge cases ===

func TestIsAutomatic_UnknownState(t *testing.T) {
	if IsAutomatic("TOTALLY_UNKNOWN_STATE") {
		t.Error("expected unknown state to be treated as interactive (false)")
	}
}

func TestIsAutomatic_KnownAutomatic(t *testing.T) {
	if !IsAutomatic(StateCheckBusinessHours) {
		t.Error("expected CHECK_BUSINESS_HOURS to be automatic")
	}
}

func TestIsAutomatic_KnownInteractive(t *testing.T) {
	if IsAutomatic(StateMainMenu) {
		t.Error("expected MAIN_MENU to be interactive (not automatic)")
	}
}

// === Interceptor auto-chain tests ===

func TestStateMachine_InterceptorAutoChains(t *testing.T) {
	m := NewMachine()

	// Interceptor returns GREETING (automatic) — should auto-chain to MAIN_MENU
	m.AddInterceptor(func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*StateResult, bool) {
		if msg.Text == "menu" {
			return NewResult(StateGreeting).WithEvent("menu_reset", nil), true
		}
		return nil, false
	})

	// GREETING (auto) → sends welcome, chains to MAIN_MENU (interactive)
	m.Register(StateGreeting, func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*StateResult, error) {
		return NewResult(StateMainMenu).
			WithText("Bienvenido! Que deseas hacer?").
			WithEvent("greeting_sent", nil), nil
	})

	// A handler for ASK_DOCUMENT that should NOT be called
	m.Register(StateAskDocument, func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*StateResult, error) {
		t.Fatal("ASK_DOCUMENT handler should not be called when interceptor fires")
		return nil, nil
	})

	sess := newTestSession(StateAskDocument)
	result, err := m.Process(context.Background(), sess, newTestMessage("menu"))
	if err != nil {
		t.Fatal(err)
	}

	// Should land on interactive MAIN_MENU after auto-chaining through GREETING
	if result.NextState != StateMainMenu {
		t.Errorf("expected MAIN_MENU after interceptor auto-chain, got %s", result.NextState)
	}

	// Should have the greeting message from GREETING handler
	if len(result.Messages) != 1 {
		t.Errorf("expected 1 message from auto-chain, got %d", len(result.Messages))
	}

	// Should have events from both interceptor and auto-chain
	if len(result.Events) != 2 {
		t.Errorf("expected 2 events (menu_reset + greeting_sent), got %d", len(result.Events))
	}
	if result.Events[0].Type != "menu_reset" {
		t.Errorf("expected first event 'menu_reset', got %q", result.Events[0].Type)
	}
	if result.Events[1].Type != "greeting_sent" {
		t.Errorf("expected second event 'greeting_sent', got %q", result.Events[1].Type)
	}
}

func TestStateMachine_InterceptorClearCtxPreserved(t *testing.T) {
	m := NewMachine()

	// Interceptor clears all context and goes to GREETING
	m.AddInterceptor(func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*StateResult, bool) {
		if msg.Text == "reset" {
			r := NewResult(StateGreeting)
			r.ClearCtx = []string{"__all__"}
			return r, true
		}
		return nil, false
	})

	m.Register(StateGreeting, func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*StateResult, error) {
		return NewResult(StateMainMenu).WithText("welcome"), nil
	})

	sess := newTestSession(StateAskDocument)
	result, err := m.Process(context.Background(), sess, newTestMessage("reset"))
	if err != nil {
		t.Fatal(err)
	}

	if result.NextState != StateMainMenu {
		t.Errorf("expected MAIN_MENU, got %s", result.NextState)
	}

	// ClearCtx __all__ from interceptor should be preserved through auto-chain
	hasAll := false
	for _, k := range result.ClearCtx {
		if k == "__all__" {
			hasAll = true
			break
		}
	}
	if !hasAll {
		t.Errorf("expected ClearCtx to contain __all__, got %v", result.ClearCtx)
	}
}

func TestMenuReset_RetryCountReset(t *testing.T) {
	interceptor := MenuResetInterceptor()
	sess := newTestSession(StateAskDocument)
	sess.RetryCount = 3

	result, intercepted := interceptor(context.Background(), sess, newTestMessage("reiniciar"))
	if !intercepted {
		t.Fatal("expected interception")
	}
	if sess.RetryCount != 0 {
		t.Errorf("expected RetryCount=0 after menu reset, got %d", sess.RetryCount)
	}
	if result.NextState != StateGreeting {
		t.Errorf("expected GREETING, got %s", result.NextState)
	}
}

// El aviso al paciente viaja en el StateResult del turno y se envía al final. En Néctar eso
// llega DESPUÉS del escalate, cuando la conversación ya es del agente y el bot no puede escribir.
// El gancho lo manda antes y el resultado final no lo repite.
func TestPreEscalationFlush_NectarSendsBeforeEscalate(t *testing.T) {
	m := NewMachine()
	var order []string
	var flushed []OutboundMessage
	m.Register(StateMainMenu, func(context.Context, *session.Session, bird.InboundMessage) (*StateResult, error) {
		return NewResult(StateEscalateToAgent).WithText("Te conecto con un asesor"), nil
	})
	var markAtEscalate string
	m.Register(StateEscalateToAgent, func(_ context.Context, sess *session.Session, _ bird.InboundMessage) (*StateResult, error) {
		order = append(order, "escalate")
		markAtEscalate = sess.GetContext(CtxEscalationNoticeFlushed)
		return NewResult(StateEscalated), nil
	})
	m.SetPreEscalationFlush(func(_ context.Context, _ *session.Session, msgs []OutboundMessage) bool {
		order = append(order, "flush")
		flushed = msgs
		return true
	})

	sess := newTestSession(StateMainMenu)
	result, err := m.Process(context.Background(), sess, newTestMessage("agente"))
	if err != nil {
		t.Fatal(err)
	}
	if len(order) != 2 || order[0] != "flush" || order[1] != "escalate" {
		t.Fatalf("el aviso sale ANTES del escalate: %v", order)
	}
	if len(flushed) != 1 {
		t.Fatalf("se entregó el aviso pendiente: %v", flushed)
	}
	if markAtEscalate != "1" {
		t.Fatalf("el handler de escalación ve la marca: %q", markAtEscalate)
	}
	if len(result.Messages) != 0 {
		t.Fatalf("lo ya enviado no se repite al final del turno: %d", len(result.Messages))
	}
	if got := sess.GetContext(CtxEscalationNoticeFlushed); got != "" {
		t.Fatalf("la marca es solo del turno y se borra al terminar Process: %q", got)
	}
}

// El vencimiento extendido que pide un paso SOBREVIVE al encadenado: ESCALATE_TO_AGENT lo pide y
// el terminal automático ESCALATED, que no hace nada, no puede perderlo. Entre dos gana el mayor.
func TestStateMachine_ExtendExpirySurvivesAutoChain(t *testing.T) {
	m := NewMachine()
	until := time.Date(2026, 10, 24, 15, 0, 0, 0, time.UTC)
	m.Register(StateEscalateToAgent, func(_ context.Context, _ *session.Session, _ bird.InboundMessage) (*StateResult, error) {
		return NewResult(StateEscalated).WithExpiryUntil(until), nil
	})
	m.Register(StateEscalated, func(_ context.Context, _ *session.Session, _ bird.InboundMessage) (*StateResult, error) {
		return NewResult(StateEscalated).WithExpiryUntil(until.Add(-time.Hour)), nil
	})

	sess := newTestSession(StateEscalateToAgent)
	result, err := m.Process(context.Background(), sess, newTestMessage("agente"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != StateEscalated {
		t.Fatalf("expected ESCALATED, got %s", result.NextState)
	}
	if !result.ExtendExpiryUntil.Equal(until) {
		t.Fatalf("ExtendExpiryUntil = %s, want %s", result.ExtendExpiryUntil, until)
	}
}

// Una clave que el paso previo escribe y el paso automático BORRA no puede volver en el
// UpdateCtx del resultado combinado: SaveState borra primero y escribe después, así que la
// clave quedaría viva (fue el caso de la marca de pedido de agente).
func TestStateMachine_AutoChainDoesNotResurrectKeyClearedByAutoStep(t *testing.T) {
	m := NewMachine()
	m.AddInterceptor(EscalationKeywordsInterceptor())
	m.Register(StateEscalateToAgent, func(_ context.Context, _ *session.Session, _ bird.InboundMessage) (*StateResult, error) {
		return NewResult(StateEscalated).WithClearCtx(CtxPatientAskedForAgent).WithContext("otra", "x"), nil
	})
	m.Register(StateEscalated, func(_ context.Context, _ *session.Session, _ bird.InboundMessage) (*StateResult, error) {
		return NewResult(StateEscalated), nil
	})

	sess := newTestSession(StateAskDocument)
	result, err := m.Process(context.Background(), sess, newTestMessage("agente"))
	if err != nil {
		t.Fatal(err)
	}
	if _, kept := result.UpdateCtx[CtxPatientAskedForAgent]; kept {
		t.Fatalf("la clave borrada por el paso automático no vuelve en UpdateCtx: %v", result.UpdateCtx)
	}
	if result.UpdateCtx["otra"] != "x" {
		t.Fatalf("lo demás se fusiona como siempre: %v", result.UpdateCtx)
	}
}
