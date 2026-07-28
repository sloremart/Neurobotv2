package recovery

import (
	"context"
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/session"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
)

// stubAI no debería invocarse en este escenario: el validador puro de REG_FIRST_NAME acepta
// "Asesor" como nombre y resuelve antes de llegar al LLM.
type stubAI struct{ calls int }

func (s *stubAI) Name() string { return "stub_ai" }

func (s *stubAI) Try(_ context.Context, _ Request) Result {
	s.calls++
	return Result{Handled: true, Message: "¿me repites tu nombre?"}
}

func newRecoverySession(state string) *session.Session {
	return &session.Session{
		ID:           "sess-recovery-1",
		PhoneNumber:  "+573001234567",
		CurrentState: state,
		Status:       session.StatusActive,
		Context:      make(map[string]string),
		CreatedAt:    time.Now(),
		ExpiresAt:    time.Now().Add(2 * time.Hour),
	}
}

// newLoopMachine arma la máquina mínima que reproduce el camino real de producción:
// estado de registro con recuperación IA opt-in + el interceptor REAL de keywords de escalación +
// una réplica del handler de escalación (escalation.go:47-51) que consulta al coordinador.
// escalations cuenta cuántas veces se entró al handler de escalación en un solo Process.
func newLoopMachine(t *testing.T, blocked string, escalations *int) *sm.Machine {
	t.Helper()
	m := sm.NewMachine()

	// Estado bloqueado: acepta cualquier texto como nombre (igual que el validador real de nombres,
	// que da por bueno "Asesor") y avanza al siguiente paso del registro.
	m.RegisterWithConfig(blocked, sm.HandlerConfig{
		InputType:    sm.InputText,
		TextValidate: func(string) bool { return true },
		AIRecovery:   true,
		Handler: func(_ context.Context, _ *session.Session, _ bird.InboundMessage) (*sm.StateResult, error) {
			return sm.NewResult("REG_SECOND_NAME"), nil
		},
	})

	// Escape a humano por palabra clave: el interceptor real del bot.
	m.AddInterceptor(sm.EscalationKeywordsInterceptor())

	// Réplica de escalation.go:47-51. El corte en 5 evita que, sin el fix, la recursión reviente el
	// stack del binario de tests: queremos que el test FALLE, no que mate al runner.
	m.Register(sm.StateEscalateToAgent, func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		*escalations++
		if *escalations > 5 {
			return sm.NewResult(sm.StateEscalated), nil
		}
		preState := sess.GetContext("_pre_auto_state")
		if preState == "" || preState == sm.StateEscalateToAgent {
			preState = sess.CurrentState
		}
		if rc := m.Recovery(); rc != nil && !rc.Active(sess) {
			if res, ok := rc.TryStart(ctx, sess, msg, preState); ok {
				return res, nil
			}
		}
		return sm.NewResult(sm.StateEscalated), nil
	})

	return m
}

// TestTryStart_EscalationKeywordDoesNotRecurse reproduce la caída de producción del 28-jul-2026.
//
// Un paciente escribe "Asesor" durante el registro. El interceptor de keywords lo manda a
// ESCALATE_TO_AGENT; el handler de escalación consulta a la capa de recuperación; TryStart revierte
// el estado al bloqueado (coordinator.go:89), el validador puro acepta "Asesor" como nombre válido
// y lo re-inyecta por machine.Process (coordinator.go:194) — que vuelve a hacer match con la misma
// keyword y re-entra al handler de escalación. Como inject() borra los flags de recuperación antes
// de re-entrar, la guarda `!rc.Active(sess)` nunca se arma y la recursión no tiene fondo: en
// producción el proceso murió por stack overflow (límite de 1 GB de stack de goroutine de Go) cada
// ~10 segundos durante casi dos horas.
func TestTryStart_EscalationKeywordDoesNotRecurse(t *testing.T) {
	const blocked = "REG_FIRST_NAME"

	var escalations int
	m := newLoopMachine(t, blocked, &escalations)

	ai := &stubAI{}
	coord := NewCoordinator(m, ai, Config{Enabled: true, MaxPatientAttempts: 2})
	m.SetRecoveryCoordinator(coord)

	sess := newRecoverySession(blocked)
	msg := bird.InboundMessage{
		ID:          "msg-asesor",
		Phone:       sess.PhoneNumber,
		MessageType: "text",
		Text:        "Asesor",
		ReceivedAt:  time.Now(),
	}

	if _, err := m.Process(context.Background(), sess, msg); err != nil {
		t.Fatalf("Process devolvió error: %v", err)
	}

	if escalations != 1 {
		t.Errorf("el handler de escalación se ejecutó %d veces, se esperaba 1: la recuperación "+
			"re-inyecta el mismo texto y vuelve a disparar la escalación (recursión infinita)", escalations)
	}
}

// TestTryStart_ExplicitHumanRequestSkipsRecovery: si el paciente pide explícitamente un humano, la
// capa de IA no debe secuestrar el pedido. Además del crash, tomarlo como dato del formulario es un
// bug de UX: "Asesor" quedaba guardado como nombre de pila del paciente.
func TestTryStart_ExplicitHumanRequestSkipsRecovery(t *testing.T) {
	const blocked = "REG_FIRST_NAME"

	var escalations int
	m := newLoopMachine(t, blocked, &escalations)

	ai := &stubAI{}
	coord := NewCoordinator(m, ai, Config{Enabled: true, MaxPatientAttempts: 2})
	m.SetRecoveryCoordinator(coord)

	sess := newRecoverySession(blocked)
	msg := bird.InboundMessage{
		ID:          "msg-asesor-2",
		Phone:       sess.PhoneNumber,
		MessageType: "text",
		Text:        "asesor",
		ReceivedAt:  time.Now(),
	}

	res, err := m.Process(context.Background(), sess, msg)
	if err != nil {
		t.Fatalf("Process devolvió error: %v", err)
	}
	if escalations != 1 {
		t.Errorf("el handler de escalación se ejecutó %d veces, se esperaba 1", escalations)
	}
	if res.NextState != sm.StateEscalated {
		t.Errorf("NextState = %q, se esperaba %q: pedir un asesor debe escalar de verdad, no entrar a recuperación IA",
			res.NextState, sm.StateEscalated)
	}
	if sess.GetContext(CtxRecoveryActive) != "" {
		t.Error("quedó activo el modo recuperación tras un pedido explícito de humano")
	}
}

// TestTryStart_RecoveryStillWorksForAmbiguousInput: el fix no debe apagar la recuperación para el
// caso para el que existe — un input ambiguo que no es pedido de humano.
func TestTryStart_RecoveryStillWorksForAmbiguousInput(t *testing.T) {
	const blocked = "ASK_DOC_TYPE"

	m := sm.NewMachine()
	m.RegisterWithConfig(blocked, sm.HandlerConfig{
		InputType:    sm.InputText,
		TextValidate: func(s string) bool { return s == "CC" },
		AIRecovery:   true,
		Handler: func(_ context.Context, _ *session.Session, _ bird.InboundMessage) (*sm.StateResult, error) {
			return sm.NewResult("ASK_DOC_NUMBER"), nil
		},
	})
	m.AddInterceptor(sm.EscalationKeywordsInterceptor())
	m.Register(sm.StateEscalateToAgent, func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		preState := sess.GetContext("_pre_auto_state")
		if preState == "" || preState == sm.StateEscalateToAgent {
			preState = sess.CurrentState
		}
		if rc := m.Recovery(); rc != nil && !rc.Active(sess) {
			if res, ok := rc.TryStart(ctx, sess, msg, preState); ok {
				return res, nil
			}
		}
		return sm.NewResult(sm.StateEscalated), nil
	})

	// La IA reformatea "cedula de ciudadania" → "CC", que sí pasa el validador puro.
	ai := &formattingAI{value: "CC"}
	coord := NewCoordinator(m, ai, Config{Enabled: true, MaxPatientAttempts: 2})
	m.SetRecoveryCoordinator(coord)

	sess := newRecoverySession(blocked)
	sess.CurrentState = sm.StateEscalateToAgent
	sess.SetContext("_pre_auto_state", blocked)

	msg := bird.InboundMessage{
		ID:          "msg-cedula",
		Phone:       sess.PhoneNumber,
		MessageType: "text",
		Text:        "cedula de ciudadania",
		ReceivedAt:  time.Now(),
	}

	res, ok := coord.TryStart(context.Background(), sess, msg, blocked)
	if !ok {
		t.Fatal("TryStart no tomó el control: la recuperación IA debe seguir funcionando para input ambiguo")
	}
	if res.NextState != "ASK_DOC_NUMBER" {
		t.Errorf("NextState = %q, se esperaba ASK_DOC_NUMBER (valor recuperado e inyectado)", res.NextState)
	}
}

// formattingAI simula al LLM devolviendo un valor ya formateado que pasa el validador puro.
type formattingAI struct{ value string }

func (f *formattingAI) Name() string { return "formatting_ai" }

func (f *formattingAI) Try(_ context.Context, _ Request) Result {
	return Result{Handled: true, Value: f.value, Reason: "formatted"}
}
