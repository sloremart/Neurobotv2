package statemachine

import (
	"context"
	"regexp"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/bird"
)

// Néctar valida el detalle con ^[a-z_]{1,64}$ y, si no casa, lo descarta con ERROR en producción.
func TestEscalationReasons_MatchTheWireShape(t *testing.T) {
	shape := regexp.MustCompile(`^[a-z_]{1,64}$`)
	seen := map[string]bool{}
	for _, reason := range EscalationReasons() {
		if !shape.MatchString(reason) {
			t.Errorf("el motivo %q no casa con ^[a-z_]{1,64}$", reason)
		}
		if seen[reason] {
			t.Errorf("el motivo %q está repetido", reason)
		}
		seen[reason] = true
	}
	for _, want := range []string{
		EscalationReasonBotDisabled, EscalationReasonAgentInputRejected, EscalationReasonNotifFreeText,
	} {
		if !seen[want] {
			t.Errorf("EscalationReasons() no incluye %q", want)
		}
	}
}

func TestValidateWithRetry_ExhaustedWritesMaxRetriesReason(t *testing.T) {
	sess := helperSess(StateAskDocument)
	sess.RetryCount = maxRetries - 1
	res := ValidateWithRetry(sess, "x", func(string) bool { return false }, "error")
	assertMaxRetries(t, res)
}

func TestValidateButtonResponse_ExhaustedWritesMaxRetriesReason(t *testing.T) {
	sess := helperSess(StateMainMenu)
	sess.RetryCount = maxRetries - 1
	res, _ := ValidateButtonResponse(sess, bird.InboundMessage{Text: "zzz"}, "a", "b")
	assertMaxRetries(t, res)
}

func TestRetryOrEscalate_ExhaustedWritesMaxRetriesReason(t *testing.T) {
	sess := helperSess(StateAskDocument)
	sess.RetryCount = maxRetries - 1
	assertMaxRetries(t, RetryOrEscalate(sess, "error"))
}

func assertMaxRetries(t *testing.T, res *StateResult) {
	t.Helper()
	if res == nil || res.NextState != StateEscalateToAgent {
		t.Fatalf("esperaba escalación, got %+v", res)
	}
	if got := res.UpdateCtx[CtxEscalationReason]; got != "max_reintentos" {
		t.Fatalf("motivo = %q, quería max_reintentos", got)
	}
}

func TestEscalationKeywordsInterceptor_WritesPatientAskedReason(t *testing.T) {
	sess := helperSess(StateAskDocument)
	res, handled := EscalationKeywordsInterceptor()(context.Background(), sess, bird.InboundMessage{Text: "asesor"})
	if !handled || res == nil {
		t.Fatal("esperaba escalación por keyword")
	}
	if got := res.UpdateCtx[CtxEscalationReason]; got != "paciente_pidio_asesor" {
		t.Fatalf("motivo = %q", got)
	}
	if got := res.UpdateCtx[CtxPatientAskedForAgent]; got != "1" {
		t.Fatalf("falta la marca del paciente: %q", got)
	}
}
