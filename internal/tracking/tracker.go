package tracking

import (
	"context"
	"log/slog"
	"time"

	localrepo "github.com/neuro-bot/neuro-bot/internal/repository/local"
	"github.com/neuro-bot/neuro-bot/internal/statemachine"
)

// EventStore is the interface for persisting events (enables testing without DB).
type EventStore interface {
	Insert(ctx context.Context, event *localrepo.ChatEvent) error
	InsertBatch(ctx context.Context, events []localrepo.ChatEvent) error
}

// EventTracker persists events from the state machine to the chat_events table.
type EventTracker struct {
	repo EventStore
}

// NewEventTracker creates a new EventTracker.
func NewEventTracker(repo EventStore) *EventTracker {
	return &EventTracker{repo: repo}
}

// LogEvent persists a single event. Errors are logged but not propagated.
func (t *EventTracker) LogEvent(ctx context.Context, sessionID, phone, eventType string, data map[string]interface{}) {
	event := &localrepo.ChatEvent{
		SessionID:   sessionID,
		PhoneNumber: phone,
		EventType:   eventType,
		EventData:   data,
		CreatedAt:   time.Now(),
	}

	if err := t.repo.Insert(ctx, event); err != nil {
		slog.Error("log event failed", "type", eventType, "error", err)
	}
}

// LogMessageSent persists a message_sent event.
func (t *EventTracker) LogMessageSent(ctx context.Context, sessionID, phone, msgType, birdMsgID string) {
	t.LogEvent(ctx, sessionID, phone, "message_sent", map[string]interface{}{
		"message_type":    msgType,
		"bird_message_id": birdMsgID,
	})
}

// LogBatch converts statemachine events to ChatEvents and persists them in a single transaction.
// Errors are logged but not propagated.
func (t *EventTracker) LogBatch(ctx context.Context, sessionID, phone string, events []statemachine.Event) {
	if len(events) == 0 {
		return
	}

	chatEvents := make([]localrepo.ChatEvent, len(events))
	now := time.Now()

	for i, e := range events {
		chatEvents[i] = localrepo.ChatEvent{
			SessionID:   sessionID,
			PhoneNumber: phone,
			EventType:   e.Type,
			EventData:   e.Data,
			CreatedAt:   now,
		}
	}

	if err := t.repo.InsertBatch(ctx, chatEvents); err != nil {
		slog.Error("log batch failed", "count", len(events), "error", err)
	}
}
