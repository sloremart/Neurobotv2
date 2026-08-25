package api

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/config"
	"github.com/neuro-bot/neuro-bot/internal/worker"
)

// H150-2: el contador de fallos de entrega NO tenia marcha atras. Se resetea solo con un
// delivered/read de un saliente, pero cuando el teléfono cruza el umbral los templates programados
// se SUPRIMEN — no sale ningun saliente, no llega ningun delivered, el contador no baja nunca.
// Para un paciente que solo RECIBE recordatorios eso es una exclusion permanente y silenciosa.
//
// El comentario de delivery_repo.go decia "o el numero escribio — prueba de WhatsApp", pero ese
// camino NO existia: ningun webhook de ENTRADA llamaba a RecordSuccess. Un mensaje entrante es la
// prueba mas barata y directa de que el numero tiene WhatsApp: no cuesta ningun envio.
func TestHandleWhatsApp_InboundMessageResetsDeliveryFailures(t *testing.T) {
	const secret = "test-secret"
	birdClient := &bird.Client{WebhookSecret: secret}
	pool := worker.NewMessageWorkerPool(1, 10)
	h := NewWebhookHandler(birdClient, pool, nil, &config.Config{})
	tracker := &mockDeliveryTracker{}
	h.SetDeliveryTracker(tracker)

	event := bird.WebhookEvent{
		Payload: bird.WebhookPayload{
			ID:        "msg-in-reset-1",
			Direction: "inbound",
			Sender:    bird.SenderInfo{Contacts: []bird.Contact{{IdentifierValue: "+573001234567"}}},
			Body:      bird.MessageBody{Type: "text", Text: bird.TextBody{Text: "Hola"}},
		},
	}
	body, _ := json.Marshal(event)
	rec := httptest.NewRecorder()
	h.HandleWhatsApp(rec, signedRequest("POST", "/api/webhooks/whatsapp", body, secret))

	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	if len(tracker.successes) != 1 {
		t.Errorf("un mensaje ENTRANTE prueba que el numero tiene WhatsApp y debe resetear el contador; "+
			"se registro %d veces (%v) — sin esto, un paciente que nunca escribe queda sin recordatorios para siempre",
			len(tracker.successes), tracker.successes)
	}
}
