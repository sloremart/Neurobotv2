package worker

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/nectar"
	"github.com/neuro-bot/neuro-bot/internal/observability"
	"github.com/neuro-bot/neuro-bot/internal/session"
	"github.com/neuro-bot/neuro-bot/internal/statemachine"
)

const testBusinessHours = "Lunes a Viernes de 7:00 a.m. a 6:00 p.m. y Sábados de 7:00 a.m. a 12:00 m."

// fakeFlowSink/captureFlowEvents montan un Tracer real con un sink en memoria como tracer global
// mientras corre fn, y lo desmontan al salir para no contaminar los tests que corren después.
type fakeFlowSink struct {
	mu     sync.Mutex
	events []domain.FlowEvent
}

func (f *fakeFlowSink) InsertBatch(_ context.Context, evs []domain.FlowEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, evs...)
	return nil
}

func captureFlowEvents(t *testing.T, fn func()) []domain.FlowEvent {
	t.Helper()
	sink := &fakeFlowSink{}
	tr := observability.New(sink, observability.LvFull)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { tr.Start(ctx); close(done) }()
	observability.Init(tr)
	teardown := sync.OnceFunc(func() {
		cancel()
		<-done
		observability.Init(nil)
	})
	t.Cleanup(teardown)

	fn()
	teardown()

	sink.mu.Lock()
	defer sink.mu.Unlock()
	return append([]domain.FlowEvent(nil), sink.events...)
}

func hasFlowEvent(events []domain.FlowEvent, flow, step string) bool {
	for _, e := range events {
		if e.Flow == flow && e.Step == step {
			return true
		}
	}
	return false
}

// templateSpySender es el cliente de mensajería que SÍ manda plantillas aprobadas (el
// `*bird.Client` real las manda; MessageSender no las declara porque solo este camino las usa).
type templateSpySender struct {
	*mockMessageSender
	tmplPhone string
	tmplSent  bird.TemplateConfig
	tmplCalls int
}

func (s *templateSpySender) SendTemplate(phone string, tmpl bird.TemplateConfig) (string, error) {
	s.tmplCalls++
	s.tmplPhone = phone
	s.tmplSent = tmpl
	return "tmpl-1", nil
}

// queueExpiredPool arma un pool con una sesión de Néctar ESCALADA y el horario cableado, igual
// que lo cablea cmd/server/main.go.
func queueExpiredPool(t *testing.T) (*MessageWorkerPool, *mockSessionMgmt, *mockMessageSender, *session.Session) {
	t.Helper()
	pool, sm, sender, sess := escalatedPool(t)
	asNectar(sess)
	pool.SetBusinessHoursText(func() string { return testBusinessHours })
	return pool, sm, sender, sess
}

// Néctar devuelve con `cola_vencida` la conversación que nadie tomó: el bot le dice al paciente
// que nadie alcanzó a atenderlo y cuándo volver, completa la sesión y cierra la conversación en
// el mismo turno.
func TestHandleAgentResume_QueueExpired(t *testing.T) {
	pool, sm, sender, sess := queueExpiredPool(t)
	sess.Context["pre_escalation_state"] = "ASK_DOCUMENT"
	sess.Context[statemachine.CtxEscalationNextDay] = "1"
	completed, resumed, markCleared := false, false, false
	sm.completeFn = func(_ context.Context, s *session.Session) error {
		completed = true
		s.Status = session.StatusCompleted
		return nil
	}
	sm.resumeFn = func(context.Context, *session.Session, string) error { resumed = true; return nil }
	sm.saveFn = func(_ context.Context, _ *session.Session, _ string, _ map[string]string, cleared []string) error {
		for _, k := range cleared {
			if k == statemachine.CtxEscalationNextDay {
				markCleared = true
			}
		}
		return nil
	}

	events := captureFlowEvents(t, func() {
		pool.processAgentCommand(context.Background(), AgentCommand{
			Action: "resume", Phone: sess.PhoneNumber,
			ReturnV1: &nectar.V1Return{Option: statemachine.ReturnValueQueueExpired},
		})
	})

	got := sentTexts(sender)
	if len(got) != 1 || got[0] != queueExpiredText(testBusinessHours) {
		t.Fatalf("esperaba UN aviso %q, got %v", queueExpiredText(testBusinessHours), got)
	}
	if !strings.Contains(got[0], testBusinessHours) {
		t.Errorf("el aviso nombra el horario: %q", got[0])
	}
	if !completed {
		t.Error("la sesión debe quedar completada: si sigue escalada, el paciente queda en el limbo")
	}
	if resumed {
		t.Error("cola_vencida cierra el caso: no reanuda la sesión en el paso bloqueado")
	}
	if !markCleared {
		t.Error("la marca de escalación encolada no sobrevive a una sesión completada")
	}
	sender.mu.Lock()
	closed := append([]string(nil), sender.closedConvs...)
	sender.mu.Unlock()
	if len(closed) != 1 || closed[0] != "conv1" {
		t.Errorf("la conversación se cierra en el mismo turno (síncrono), got %v", closed)
	}
	if !hasFlowEvent(events, "escalacion", "cola_vencida") {
		t.Errorf("falta el evento escalacion/cola_vencida: %+v", events)
	}
}

// Fuera de la ventana de 24 h de WhatsApp el texto libre no sale (409 window_closed) y solo
// queda la plantilla aprobada. El cierre ocurre igual.
func TestNotifyQueueExpired_WindowClosedUsesTemplate(t *testing.T) {
	pool, sm, base, sess := queueExpiredPool(t)
	base.sendErr = nectar.ErrWindowClosed
	sender := &templateSpySender{mockMessageSender: base}
	pool.SetDependencies(sm, sender, &mockMessageProcessor{})
	completed := false
	sm.completeFn = func(_ context.Context, s *session.Session) error {
		completed = true
		s.Status = session.StatusCompleted
		return nil
	}

	pool.processAgentCommand(context.Background(), AgentCommand{
		Action: "resume", Phone: sess.PhoneNumber, ReturnValue: statemachine.ReturnValueQueueExpired,
	})

	if sender.tmplCalls != 1 {
		t.Fatalf("con la ventana cerrada debe salir la plantilla, got %d envíos", sender.tmplCalls)
	}
	if sender.tmplSent.ProjectID != queueExpiredTemplateProject {
		t.Errorf("plantilla con project_id %q, esperaba %q", sender.tmplSent.ProjectID, queueExpiredTemplateProject)
	}
	if len(sender.tmplSent.Params) != 1 || sender.tmplSent.Params[0].Value != testBusinessHours {
		t.Errorf("la plantilla lleva el horario como parámetro, got %+v", sender.tmplSent.Params)
	}
	if sender.tmplPhone != testNectarPhone {
		t.Errorf("plantilla a %q, esperaba %q", sender.tmplPhone, testNectarPhone)
	}
	if !completed {
		t.Error("la sesión se cierra aunque el aviso haya tenido que irse por plantilla")
	}
}
