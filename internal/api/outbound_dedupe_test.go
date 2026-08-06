package api

import (
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/config"
	"github.com/neuro-bot/neuro-bot/internal/worker"
)

// La suscripción con eventos de ESTADO entrega 4-5 eventos por mensaje (accepted → processing →
// sent → delivered/read). El trabajo caro del handler (FetchMessageText para detectar /bot,
// TouchAgentActivity) debe correr UNA vez por mensaje; el registro de estado corre por evento.
func TestHandleOutbound_ExpensiveWorkOncePerMessageID(t *testing.T) {
	tracker := &mockDeliveryTracker{}
	birdClient := &bird.Client{} // sin HTTP: un FetchMessageText no-dedupeado haría panic
	pool := worker.NewMessageWorkerPool(1, 10)
	h := NewWebhookHandler(birdClient, pool, nil, &config.Config{})
	h.SetDeliveryTracker(tracker)

	mkEvent := func(status, text string) bird.WebhookEvent {
		return bird.WebhookEvent{
			Payload: bird.WebhookPayload{
				ID:        "msg-lifecycle-1",
				Direction: "outgoing",
				Status:    status,
				Receiver:  bird.ReceiverInfo{Connector: bird.Contact{IdentifierValue: "+573001234567"}},
				Body:      bird.MessageBody{Type: "text", Text: bird.TextBody{Text: text}},
			},
		}
	}

	// Primer evento del mensaje: trae texto, hace el trabajo completo.
	h.handleOutbound(mkEvent("accepted", "/bot ping"))
	// Eventos de estado posteriores del MISMO mensaje: SIN texto — si el dedupe no corta, el
	// handler intentaría FetchMessageText contra un cliente sin HTTP y este test entra en pánico.
	h.handleOutbound(mkEvent("processing", ""))
	h.handleOutbound(mkEvent("sent", ""))
	h.handleOutbound(mkEvent("delivered", ""))

	// El registro de entrega SÍ corre por evento: delivered debe haberse registrado.
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	if len(tracker.successes) != 1 {
		t.Errorf("delivered debe registrarse aunque el mensaje ya se haya procesado, got %v", tracker.successes)
	}
}
