package worker

import (
	"context"
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/session"
	"github.com/neuro-bot/neuro-bot/internal/tracking"
)

// C-04: lo que el paciente escribe mientras espera al agente no va en claro a chat_events, solo su
// largo (el dashboard cuenta las filas de msg_during_escalation, no lee el texto).
func TestMsgDuringEscalationEvent_StoresOnlyLength(t *testing.T) {
	sm := newMockSessionMgmt()
	sm.findOrCreateFn = func(_ context.Context, phone string) (*session.Session, bool, error) {
		return &session.Session{
			ID: "sess-esc-pii", PhoneNumber: phone, CurrentState: "ESCALATED",
			ConversationID: "conv-esc-pii", Status: session.StatusEscalated,
			Context: map[string]string{"pre_escalation_state": "ASK_DOCUMENT"},
		}, false, nil
	}
	eventStore := &mockEventStoreWorker{}
	pool := NewMessageWorkerPool(1, 10)
	pool.SetDependencies(sm, &mockMessageSender{}, &mockMessageProcessor{})
	pool.SetTracker(tracking.NewEventTracker(eventStore))

	pool.processMessage(context.Background(), bird.InboundMessage{
		ID: "msg-esc-pii", Phone: "+573001234567", MessageType: "text",
		Text: "1000000689 Juan Pérez", ReceivedAt: time.Now(),
	})

	eventStore.mu.Lock()
	defer eventStore.mu.Unlock()
	for _, ev := range eventStore.insertedEvents {
		if ev.EventType != "msg_during_escalation" {
			continue
		}
		if got := ev.EventData["text"]; got != "[redactado: 21 caracteres]" {
			t.Fatalf("msg_during_escalation debe guardar solo el largo, got %v", got)
		}
		if ev.EventData["type"] != "text" {
			t.Fatalf("el tipo de mensaje se conserva: %v", ev.EventData)
		}
		return
	}
	t.Fatalf("no salió msg_during_escalation: %+v", eventStore.insertedEvents)
}
