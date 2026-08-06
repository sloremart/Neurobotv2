package api

import (
	"context"
	"sync"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/config"
	"github.com/neuro-bot/neuro-bot/internal/worker"
)

// El webhook outbound de Bird trae el estado de entrega. Registrarlo es lo que permite dejar de
// pagar templates a números sin WhatsApp: fallo → contador; entregado/leído → reset.

type mockDeliveryTracker struct {
	mu        sync.Mutex
	failures  []string // phone|status
	successes []string
}

func (m *mockDeliveryTracker) RecordFailure(_ context.Context, phone, status string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failures = append(m.failures, phone+"|"+status)
	return nil
}

func (m *mockDeliveryTracker) RecordSuccess(_ context.Context, phone string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.successes = append(m.successes, phone)
	return nil
}

func outboundEventWithStatus(status string) bird.WebhookEvent {
	return bird.WebhookEvent{
		Payload: bird.WebhookPayload{
			ID:        "msg-status-1",
			Direction: "outgoing",
			Status:    status,
			Receiver: bird.ReceiverInfo{
				Connector: bird.Contact{IdentifierValue: "+573001234567"},
			},
			// Texto "/bot": evita el FetchMessageText por API y el TouchAgentActivity (el fixture
			// no cablea sessionManager); el registro de entrega ocurre ANTES de ambos.
			Body: bird.MessageBody{Type: "text", Text: bird.TextBody{Text: "/bot ping"}},
		},
	}
}

func newStatusHandler(tracker *mockDeliveryTracker) *WebhookHandler {
	birdClient := &bird.Client{}
	pool := worker.NewMessageWorkerPool(1, 10)
	h := NewWebhookHandler(birdClient, pool, nil, &config.Config{})
	h.SetDeliveryTracker(tracker)
	return h
}

func TestHandleOutbound_DeliveryFailedRecorded(t *testing.T) {
	tracker := &mockDeliveryTracker{}
	h := newStatusHandler(tracker)

	h.handleOutbound(outboundEventWithStatus("delivery_failed"))

	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	if len(tracker.failures) != 1 || tracker.failures[0] != "+573001234567|delivery_failed" {
		t.Errorf("esperaba 1 fallo registrado, got %v", tracker.failures)
	}
}

func TestHandleOutbound_DeliveredResetsCounter(t *testing.T) {
	tracker := &mockDeliveryTracker{}
	h := newStatusHandler(tracker)

	h.handleOutbound(outboundEventWithStatus("delivered"))

	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	if len(tracker.successes) != 1 {
		t.Errorf("esperaba 1 éxito registrado, got %v", tracker.successes)
	}
	if len(tracker.failures) != 0 {
		t.Errorf("no debía registrar fallos, got %v", tracker.failures)
	}
}

// Estados intermedios (accepted/sent) no dicen nada de la entrega: no tocan el contador.
func TestHandleOutbound_IntermediateStatusIgnored(t *testing.T) {
	tracker := &mockDeliveryTracker{}
	h := newStatusHandler(tracker)

	h.handleOutbound(outboundEventWithStatus("accepted"))
	h.handleOutbound(outboundEventWithStatus("sent"))
	h.handleOutbound(outboundEventWithStatus(""))

	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	if len(tracker.failures)+len(tracker.successes) != 0 {
		t.Errorf("estados intermedios no deben registrar nada: fallos=%v exitos=%v", tracker.failures, tracker.successes)
	}
}
