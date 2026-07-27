package worker

import (
	"context"
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/session"
	"github.com/neuro-bot/neuro-bot/internal/statemachine"
)

// TestSaveState_SurvivesExhaustedMessageBudget cubre el bug de producción 2026-07-27: cuando el handler
// consumía todo el presupuesto del mensaje (OCR colgado), la persistencia posterior heredaba un contexto
// YA VENCIDO y fallaba con "context deadline exceeded" — el paciente perdía el estado de su sesión
// (save_state_error) y encima el rescate (Complete) fallaba por la misma razón.
//
// El estado del paciente es lo último que se debe perder: SaveState debe correr con su propio contexto,
// independiente del presupuesto del mensaje.
func TestSaveState_SurvivesExhaustedMessageBudget(t *testing.T) {
	sm := newMockSessionMgmt()

	var saveCtxErr error
	var saved bool
	sm.saveFn = func(ctx context.Context, _ *session.Session, _ string, _ map[string]string, _ []string) error {
		saved = true
		saveCtxErr = ctx.Err() // debe ser nil: contexto propio, vivo
		return nil
	}

	sender := &mockMessageSender{}
	processor := &mockMessageProcessor{
		processFn: func(_ context.Context, _ *session.Session, _ bird.InboundMessage) (*statemachine.StateResult, error) {
			return &statemachine.StateResult{
				NextState: "CONFIRM_OCR_RESULT",
				Messages:  []statemachine.OutboundMessage{&statemachine.TextMessage{Text: "listo"}},
				UpdateCtx: map[string]string{"ocr_cups_json": "[]"},
			}, nil
		},
	}

	pool := NewMessageWorkerPool(1, 10)
	pool.SetDependencies(sm, sender, processor)

	sess := &session.Session{ID: "sess-budget", PhoneNumber: "+573001234567", CurrentState: "UPLOAD_MEDICAL_ORDER"}

	// Contexto YA vencido: reproduce el mensaje que agotó su presupuesto en el OCR.
	dead, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-dead.Done()

	pool.sendAndSave(dead, sess, sess.PhoneNumber, &statemachine.StateResult{
		NextState: "CONFIRM_OCR_RESULT",
		Messages:  []statemachine.OutboundMessage{&statemachine.TextMessage{Text: "listo"}},
		UpdateCtx: map[string]string{"ocr_cups_json": "[]"},
	})

	if !saved {
		t.Fatal("SaveState no se invocó: el estado del paciente se perdió")
	}
	if saveCtxErr != nil {
		t.Fatalf("SaveState recibió un contexto ya vencido (%v): la persistencia debe tener presupuesto propio", saveCtxErr)
	}
}

// TestSaveStateFallback_UsesFreshContext: si SaveState falla, el rescate marca la sesión como completada
// para que el siguiente mensaje arranque limpio en vez de cargar estado obsoleto. Ese rescate NO puede
// heredar el contexto vencido — en producción fallaba SIEMPRE por construcción
// (save_state_fallback_complete_failed), justo cuando más se necesitaba.
func TestSaveStateFallback_UsesFreshContext(t *testing.T) {
	sm := newMockSessionMgmt()
	sm.saveFn = func(_ context.Context, _ *session.Session, _ string, _ map[string]string, _ []string) error {
		return context.DeadlineExceeded
	}

	var completeCtxErr error
	var completed bool
	sm.completeFn = func(ctx context.Context, _ *session.Session) error {
		completed = true
		completeCtxErr = ctx.Err()
		return nil
	}

	sender := &mockMessageSender{}
	pool := NewMessageWorkerPool(1, 10)
	pool.SetDependencies(sm, sender, &mockMessageProcessor{})

	sess := &session.Session{ID: "sess-fallback", PhoneNumber: "+573007654321", CurrentState: "UPLOAD_MEDICAL_ORDER"}

	dead, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-dead.Done()

	pool.sendAndSave(dead, sess, sess.PhoneNumber, &statemachine.StateResult{
		NextState: "CONFIRM_OCR_RESULT",
		Messages:  []statemachine.OutboundMessage{&statemachine.TextMessage{Text: "listo"}},
	})

	if !completed {
		t.Fatal("el rescate (Complete) no se invocó tras fallar SaveState")
	}
	if completeCtxErr != nil {
		t.Fatalf("el rescate corrió con un contexto ya vencido (%v): no puede funcionar nunca", completeCtxErr)
	}
}
