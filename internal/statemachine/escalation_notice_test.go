package statemachine

import (
	"context"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/bird"
)

// El envío directo redundante del handler de escalación fue eliminado (~700 msgs/sem duplicados):
// ahora CADA ruta de escalación debe llevar su aviso al paciente en el RESULT. Las rutas que ya
// tenían texto contextual propio no cambian; las que escalaban mudas (reintentos máximos, palabra
// "asesor") deben incluir el aviso genérico.

func escalationNoticeIn(msgs []OutboundMessage) bool {
	for _, m := range msgs {
		if t, ok := m.(*TextMessage); ok && t.Text == EscalationNoticeText {
			return true
		}
	}
	return false
}

func TestValidateWithRetry_EscalationCarriesNotice(t *testing.T) {
	sess := helperSess(StateAskDocument)
	sess.RetryCount = maxRetries - 1

	result := ValidateWithRetry(sess, "xxx", func(string) bool { return false }, "err")
	if result == nil || result.NextState != StateEscalateToAgent {
		t.Fatalf("esperaba escalación, got %+v", result)
	}
	if !escalationNoticeIn(result.Messages) {
		t.Error("la escalación por reintentos debe avisar al paciente en el result (antes lo hacía el envío directo eliminado)")
	}
}

func TestValidateButtonResponse_EscalationCarriesNotice(t *testing.T) {
	sess := helperSess(StateAskDocument)
	sess.RetryCount = maxRetries - 1

	result, _ := ValidateButtonResponse(sess, bird.InboundMessage{Text: "no válido"}, "op1", "op2")
	if result == nil || result.NextState != StateEscalateToAgent {
		t.Fatalf("esperaba escalación, got %+v", result)
	}
	if !escalationNoticeIn(result.Messages) {
		t.Error("la escalación por reintentos de botones debe avisar al paciente en el result")
	}
}

func TestRetryOrEscalate_EscalationCarriesNotice(t *testing.T) {
	sess := helperSess(StateAskDocument)
	sess.RetryCount = maxRetries - 1

	result := RetryOrEscalate(sess, "err")
	if result.NextState != StateEscalateToAgent {
		t.Fatalf("esperaba escalación, got %s", result.NextState)
	}
	if !escalationNoticeIn(result.Messages) {
		t.Error("RetryOrEscalate debe avisar al paciente en el result")
	}
}

func TestEscalationKeywordInterceptor_CarriesNotice(t *testing.T) {
	sess := helperSess(StateAskDocument)
	interceptor := EscalationKeywordsInterceptor()

	result, handled := interceptor(context.Background(), sess, bird.InboundMessage{Text: "quiero hablar con un asesor"})
	if !handled || result == nil || result.NextState != StateEscalateToAgent {
		t.Fatalf("esperaba escalación por keyword, got handled=%v result=%+v", handled, result)
	}
	if !escalationNoticeIn(result.Messages) {
		t.Error("la escalación por palabra clave debe avisar al paciente en el result")
	}
}
