package statemachine

import (
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/session"
)

func helperSess(state string) *session.Session {
	return &session.Session{
		ID:           "sess-h-1",
		PhoneNumber:  "+573001234567",
		CurrentState: state,
		Status:       session.StatusActive,
		Context:      make(map[string]string),
		CreatedAt:    time.Now(),
		ExpiresAt:    time.Now().Add(2 * time.Hour),
	}
}

// ==================== ValidateWithRetry ====================

func TestValidateWithRetry_ValidInput(t *testing.T) {
	sess := helperSess(StateAskDocument)
	sess.RetryCount = 2 // was retrying

	result := ValidateWithRetry(sess, "1234567890", func(s string) bool { return len(s) == 10 }, "error")
	if result != nil {
		t.Error("expected nil for valid input")
	}
	if sess.RetryCount != 0 {
		t.Errorf("expected RetryCount reset to 0, got %d", sess.RetryCount)
	}
}

func TestValidateWithRetry_InvalidRetry1(t *testing.T) {
	sess := helperSess(StateAskDocument)
	sess.RetryCount = 0

	result := ValidateWithRetry(sess, "abc", func(s string) bool { return false }, "Invalid input")
	if result == nil {
		t.Fatal("expected non-nil result for invalid input")
	}
	if result.NextState != StateAskDocument {
		t.Errorf("expected same state, got %s", result.NextState)
	}
	if sess.RetryCount != 1 {
		t.Errorf("expected RetryCount=1, got %d", sess.RetryCount)
	}
}

func TestValidateWithRetry_InvalidRetry2(t *testing.T) {
	sess := helperSess(StateAskDocument)
	sess.RetryCount = 1

	result := ValidateWithRetry(sess, "abc", func(s string) bool { return false }, "Invalid input")
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.NextState != StateAskDocument {
		t.Errorf("expected same state, got %s", result.NextState)
	}
	if sess.RetryCount != 2 {
		t.Errorf("expected RetryCount=2, got %d", sess.RetryCount)
	}
}

func TestValidateWithRetry_MaxRetriesEscalates(t *testing.T) {
	sess := helperSess(StateAskDocument)
	sess.RetryCount = 2 // next will be 3 == maxRetries

	result := ValidateWithRetry(sess, "abc", func(s string) bool { return false }, "Invalid input")
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.NextState != StateEscalateToAgent {
		t.Errorf("expected ESCALATE_TO_AGENT, got %s", result.NextState)
	}
	if sess.RetryCount != 0 {
		t.Error("expected RetryCount reset after escalation")
	}
}

func TestValidateWithRetry_ErrorMessage(t *testing.T) {
	sess := helperSess(StateAskDocument)
	result := ValidateWithRetry(sess, "abc", func(s string) bool { return false }, "Please enter digits only")
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if len(result.Messages) == 0 {
		t.Fatal("expected error message")
	}
}

// ==================== ValidateButtonResponse ====================

func TestValidateButton_ValidPostback(t *testing.T) {
	sess := helperSess(StateMainMenu)
	msg := bird.InboundMessage{
		ID: "msg-1", Phone: "+573001234567",
		MessageType: "text", IsPostback: true, PostbackPayload: "consultar",
		ReceivedAt: time.Now(),
	}

	result, selected := ValidateButtonResponse(sess, msg, "consultar", "agendar", "agente")
	if result != nil {
		t.Error("expected nil result for valid postback")
	}
	if selected != "consultar" {
		t.Errorf("expected 'consultar', got %q", selected)
	}
}

func TestValidateButton_NumericInput(t *testing.T) {
	sess := helperSess(StateMainMenu)

	tests := []struct {
		input    string
		expected string
	}{
		{"1", "consultar"},
		{"2", "agendar"},
		{"3", "agente"},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			sess.RetryCount = 0
			msg := bird.InboundMessage{
				ID: "msg-1", Phone: "+573001234567",
				MessageType: "text", Text: tc.input,
				ReceivedAt: time.Now(),
			}
			result, selected := ValidateButtonResponse(sess, msg, "consultar", "agendar", "agente")
			if result != nil {
				t.Error("expected nil result for valid numeric input")
			}
			if selected != tc.expected {
				t.Errorf("expected %q, got %q", tc.expected, selected)
			}
		})
	}
}

func TestValidateButton_OutOfRange(t *testing.T) {
	sess := helperSess(StateMainMenu)
	msg := bird.InboundMessage{
		ID: "msg-1", Phone: "+573001234567",
		MessageType: "text", Text: "4",
		ReceivedAt: time.Now(),
	}
	result, selected := ValidateButtonResponse(sess, msg, "consultar", "agendar", "agente")
	if result == nil {
		t.Fatal("expected retry result for out-of-range")
	}
	if selected != "" {
		t.Errorf("expected empty selected, got %q", selected)
	}
}

func TestValidateButton_InvalidText(t *testing.T) {
	sess := helperSess(StateMainMenu)
	msg := bird.InboundMessage{
		ID: "msg-1", Phone: "+573001234567",
		MessageType: "text", Text: "hello world",
		ReceivedAt: time.Now(),
	}
	result, _ := ValidateButtonResponse(sess, msg, "consultar", "agendar", "agente")
	if result == nil {
		t.Fatal("expected retry result")
	}
	if result.NextState != StateMainMenu {
		t.Errorf("expected same state, got %s", result.NextState)
	}
}

func TestValidateButton_MaxRetriesEscalates(t *testing.T) {
	sess := helperSess(StateMainMenu)
	sess.RetryCount = 2 // next = 3 = maxRetries

	msg := bird.InboundMessage{
		ID: "msg-1", Phone: "+573001234567",
		MessageType: "text", Text: "garbage",
		ReceivedAt: time.Now(),
	}
	result, _ := ValidateButtonResponse(sess, msg, "consultar", "agendar")
	if result == nil {
		t.Fatal("expected escalation result")
	}
	if result.NextState != StateEscalateToAgent {
		t.Errorf("expected ESCALATE_TO_AGENT, got %s", result.NextState)
	}
	if sess.RetryCount != 0 {
		t.Error("expected RetryCount reset after escalation")
	}
}

func TestValidateButton_InvalidPostback(t *testing.T) {
	sess := helperSess(StateMainMenu)
	msg := bird.InboundMessage{
		ID: "msg-1", Phone: "+573001234567",
		MessageType: "text", IsPostback: true, PostbackPayload: "unknown_action",
		ReceivedAt: time.Now(),
	}
	result, selected := ValidateButtonResponse(sess, msg, "consultar", "agendar")
	if result == nil {
		t.Fatal("expected retry result for invalid postback")
	}
	if selected != "" {
		t.Errorf("expected empty selected, got %q", selected)
	}
}

// ==================== RetryOrEscalate ====================

func TestRetryOrEscalate_FirstRetry(t *testing.T) {
	sess := helperSess(StateAskDocument)
	sess.RetryCount = 0

	result := RetryOrEscalate(sess, "Try again.")
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.NextState != StateAskDocument {
		t.Errorf("expected same state, got %s", result.NextState)
	}
	if sess.RetryCount != 1 {
		t.Errorf("expected RetryCount=1, got %d", sess.RetryCount)
	}
}

func TestRetryOrEscalate_SecondRetry(t *testing.T) {
	sess := helperSess(StateAskDocument)
	sess.RetryCount = 1

	result := RetryOrEscalate(sess, "Try again.")
	if result.NextState != StateAskDocument {
		t.Errorf("expected same state, got %s", result.NextState)
	}
	if sess.RetryCount != 2 {
		t.Errorf("expected RetryCount=2, got %d", sess.RetryCount)
	}
}

func TestRetryOrEscalate_Escalates(t *testing.T) {
	sess := helperSess(StateAskDocument)
	sess.RetryCount = 2

	result := RetryOrEscalate(sess, "Try again.")
	if result.NextState != StateEscalateToAgent {
		t.Errorf("expected ESCALATE_TO_AGENT, got %s", result.NextState)
	}
	if sess.RetryCount != 0 {
		t.Error("expected RetryCount reset after escalation")
	}
}

func TestRetryOrEscalate_ErrorMessage(t *testing.T) {
	sess := helperSess(StateAskDocument)
	result := RetryOrEscalate(sess, "Custom error msg.")
	if len(result.Messages) == 0 {
		t.Fatal("expected at least 1 message")
	}
	txt, ok := result.Messages[0].(*TextMessage)
	if !ok {
		t.Fatal("expected TextMessage")
	}
	if txt.Text != "Custom error msg." {
		t.Errorf("expected custom msg, got %q", txt.Text)
	}
}

// ==================== ValidateSearchCount ====================

func TestValidateSearchCount_None(t *testing.T) {
	sess := helperSess(StateRegMunicipality)
	outcome, result := ValidateSearchCount(sess, 0, 5, "No match.", "Too many.")
	if outcome != SearchNone {
		t.Errorf("expected SearchNone, got %d", outcome)
	}
	if result == nil {
		t.Fatal("expected non-nil result for 0 matches")
	}
}

func TestValidateSearchCount_Exact(t *testing.T) {
	sess := helperSess(StateRegMunicipality)
	outcome, result := ValidateSearchCount(sess, 1, 5, "No match.", "Too many.")
	if outcome != SearchExact {
		t.Errorf("expected SearchExact, got %d", outcome)
	}
	if result != nil {
		t.Error("expected nil result for exact match")
	}
}

func TestValidateSearchCount_Multiple(t *testing.T) {
	sess := helperSess(StateRegMunicipality)
	outcome, result := ValidateSearchCount(sess, 3, 5, "No match.", "Too many.")
	if outcome != SearchMultiple {
		t.Errorf("expected SearchMultiple, got %d", outcome)
	}
	if result != nil {
		t.Error("expected nil result for multiple matches within limit")
	}
}

func TestValidateSearchCount_AtLimit(t *testing.T) {
	sess := helperSess(StateRegMunicipality)
	outcome, result := ValidateSearchCount(sess, 5, 5, "No match.", "Too many.")
	if outcome != SearchMultiple {
		t.Errorf("expected SearchMultiple, got %d", outcome)
	}
	if result != nil {
		t.Error("expected nil result for count == maxDisplay")
	}
}

func TestValidateSearchCount_TooMany(t *testing.T) {
	sess := helperSess(StateRegMunicipality)
	outcome, result := ValidateSearchCount(sess, 6, 5, "No match.", "Too many.")
	if outcome != SearchTooMany {
		t.Errorf("expected SearchTooMany, got %d", outcome)
	}
	if result == nil {
		t.Fatal("expected non-nil result for too many matches")
	}
}

func TestValidateSearchCount_NoneEscalates(t *testing.T) {
	sess := helperSess(StateRegMunicipality)
	sess.RetryCount = 2
	_, result := ValidateSearchCount(sess, 0, 5, "No match.", "Too many.")
	if result.NextState != StateEscalateToAgent {
		t.Errorf("expected ESCALATE_TO_AGENT, got %s", result.NextState)
	}
}

func TestValidateSearchCount_TooManyEscalates(t *testing.T) {
	sess := helperSess(StateRegMunicipality)
	sess.RetryCount = 2
	_, result := ValidateSearchCount(sess, 20, 5, "No match.", "Too many.")
	if result.NextState != StateEscalateToAgent {
		t.Errorf("expected ESCALATE_TO_AGENT, got %s", result.NextState)
	}
}

// §8.2: invalid_input de estados de BOTONES debe registrar el texto/payload (antes 70% llegaba vacío).
func TestValidateButtonResponse_LogsInput(t *testing.T) {
	sess := &session.Session{CurrentState: "MAIN_MENU", Context: map[string]string{}}
	res, _ := ValidateButtonResponse(sess, bird.InboundMessage{MessageType: "text", Text: "quiero cita"}, "opt_a")
	if res == nil {
		t.Fatal("esperaba retry result")
	}
	ev := res.Events[0]
	if ev.Data["input"] != "quiero cita" {
		t.Errorf("invalid_input sin el texto: %v", ev.Data)
	}
	// Postback inválido → registra el payload.
	sess2 := &session.Session{CurrentState: "MAIN_MENU", Context: map[string]string{}}
	res2, _ := ValidateButtonResponse(sess2, bird.InboundMessage{MessageType: "text", IsPostback: true, PostbackPayload: "regimen_2"}, "opt_a")
	if res2.Events[0].Data["input"] != "regimen_2" {
		t.Errorf("invalid_input sin payload: %v", res2.Events[0].Data)
	}
}
