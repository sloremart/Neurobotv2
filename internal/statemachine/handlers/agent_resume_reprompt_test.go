package handlers

import (
	"context"
	"strings"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/session"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
	"github.com/neuro-bot/neuro-bot/internal/testutil"
)

// Un retorno sin dato del agente de Néctar (`__resume__`) re-muestra el paso sin gastar un
// reintento del paciente; Bird sigue exactamente como antes.

func resumeTurnSess(state string, nectar bool) *session.Session {
	sess := testSess(state)
	if nectar {
		sess.PhoneNumber = "nectar:12"
		sess.SetContext(sm.CtxAgentResumeTurn, "1")
	}
	return sess
}

func resumeMsg() bird.InboundMessage { return textM("__resume__") }

func TestCoverageMenuReturn_MainMenuRePromptsWithoutRetry(t *testing.T) {
	m := sm.NewMachine()
	RegisterGreetingHandlers(m, sampleConfig(), nil)
	sess := resumeTurnSess(sm.StateMainMenu, true)
	sess.RetryCount = 1

	result, err := m.Process(context.Background(), sess, resumeMsg())
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateMainMenu {
		t.Fatalf("NextState = %s", result.NextState)
	}
	if sess.RetryCount != 1 {
		t.Errorf("RetryCount = %d, no debe subir", sess.RetryCount)
	}
	if len(result.Messages) == 0 {
		t.Error("debe re-dibujar el menú")
	}
}

func TestAskClientType_NectarResumeRePromptsWithoutRetry(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateAskClientType, askClientTypeHandler(&testutil.MockEntityRepo{}))
	sess := resumeTurnSess(sm.StateAskClientType, true)
	sess.RetryCount = 1

	result, err := m.Process(context.Background(), sess, resumeMsg())
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateAskClientType || sess.RetryCount != 1 {
		t.Fatalf("next=%s retry=%d", result.NextState, sess.RetryCount)
	}
	if len(result.Messages) != 1 {
		t.Fatalf("debe quedar solo la lista de tipos, hay %d mensajes", len(result.Messages))
	}
	if _, ok := result.Messages[0].(*sm.ListMessage); !ok {
		t.Errorf("quería la lista de tipos de entidad, obtuve %T", result.Messages[0])
	}
}

func TestAskClientType_BirdResumeStillCountsRetry(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateAskClientType, askClientTypeHandler(&testutil.MockEntityRepo{}))
	sess := resumeTurnSess(sm.StateAskClientType, false)

	if _, err := m.Process(context.Background(), sess, resumeMsg()); err != nil {
		t.Fatal(err)
	}
	if sess.RetryCount != 1 {
		t.Errorf("Bird no cambia: RetryCount = %d, quería 1", sess.RetryCount)
	}
}

func TestAskManualCups_NectarResumeDoesNotSearchTheKeyword(t *testing.T) {
	searched := false
	procRepo := &mockProcedureRepo{
		searchByNameFn: func(_ context.Context, _ string) ([]domain.Procedure, error) {
			searched = true
			return nil, nil
		},
	}
	m := sm.NewMachine()
	m.Register(sm.StateAskManualCups, askManualCupsHandler(procRepo))
	sess := resumeTurnSess(sm.StateAskManualCups, true)

	result, err := m.Process(context.Background(), sess, resumeMsg())
	if err != nil {
		t.Fatal(err)
	}
	if searched {
		t.Fatal("__resume__ se buscó como nombre de procedimiento")
	}
	if result.NextState != sm.StateAskManualCups || sess.RetryCount != 0 {
		t.Fatalf("next=%s retry=%d", result.NextState, sess.RetryCount)
	}
	for _, msg := range result.Messages {
		if tm, ok := msg.(*sm.TextMessage); ok && strings.Contains(tm.Text, "No encontramos") {
			t.Errorf("texto de «no encontrado» tras un retorno: %q", tm.Text)
		}
	}
}

func TestAskManualCups_BirdResumeUnchanged(t *testing.T) {
	searched := false
	procRepo := &mockProcedureRepo{
		searchByNameFn: func(_ context.Context, _ string) ([]domain.Procedure, error) {
			searched = true
			return nil, nil
		},
	}
	m := sm.NewMachine()
	m.Register(sm.StateAskManualCups, askManualCupsHandler(procRepo))

	if _, err := m.Process(context.Background(), resumeTurnSess(sm.StateAskManualCups, false), resumeMsg()); err != nil {
		t.Fatal(err)
	}
	if !searched {
		t.Error("Bird conserva su comportamiento: busca el texto tal cual")
	}
}

func TestRegBirthDate_NectarResumeRePromptsWithoutRetryOrComplaint(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateRegBirthDate, regBirthDateHandler())
	sess := resumeTurnSess(sm.StateRegBirthDate, true)
	sess.RetryCount = 1

	result, err := m.Process(context.Background(), sess, resumeMsg())
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateRegBirthDate || sess.RetryCount != 1 {
		t.Fatalf("next=%s retry=%d", result.NextState, sess.RetryCount)
	}
	for _, msg := range result.Messages {
		if tm, ok := msg.(*sm.TextMessage); ok && strings.Contains(tm.Text, "No entendí") {
			t.Errorf("queja al paciente que no escribió nada: %q", tm.Text)
		}
	}
}
