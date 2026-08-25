package api

import (
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/bird"
)

// H150 (reportado por el equipo de hdg-bot). recordDeliveryStatus corre ANTES del dedupe por
// Payload.ID —a proposito, porque los estados de un mismo mensaje difieren y todos deben contar—,
// pero eso deja el contador de fallos consecutivos sin ninguna proteccion contra el MISMO evento
// entregado dos veces. Con deliveryFailureThreshold=2, dos copias de un unico delivery_failed
// bastan para suprimir los templates programados a ese numero: el paciente se queda sin
// recordatorios de cita, en silencio, y nada reinicia el contador si nunca escribe.
//
// La firma no lo impide: es valida durante la ventana de bird/webhook.go (24h con math.Abs
// alrededor = 48h efectivas), asi que los bytes de una peticion legitima valen dos veces.
func statusEvent(id, status string) bird.WebhookEvent {
	return bird.WebhookEvent{
		Payload: bird.WebhookPayload{
			ID:        id,
			Direction: "outgoing",
			Status:    status,
			Receiver:  bird.ReceiverInfo{Connector: bird.Contact{IdentifierValue: "+573001234567"}},
			// "/bot": evita FetchMessageText (el fixture no tiene HTTP) y TouchAgentActivity.
			Body: bird.MessageBody{Type: "text", Text: bird.TextBody{Text: "/bot ping"}},
		},
	}
}

func TestHandleOutbound_ReplayedFailureCountsOnce(t *testing.T) {
	tracker := &mockDeliveryTracker{}
	h := newStatusHandler(tracker)

	ev := statusEvent("msg-replay-1", "delivery_failed")
	h.handleOutbound(ev) // entrega legitima
	h.handleOutbound(ev) // MISMO evento reenviado: mismos bytes, misma firma, mismo sello

	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	if len(tracker.failures) != 1 {
		t.Errorf("un unico delivery_failed reenviado debe contar UNA vez; conto %d veces (%v) — "+
			"con umbral 2 eso deja al paciente sin recordatorios", len(tracker.failures), tracker.failures)
	}
}

// Guardarrail de la correccion: dedupear por (ID, estado) NO puede matar la supresion. Cada estado
// distinto del mismo mensaje sigue contando una vez — que es justo lo que rompia la alternativa de
// mover recordDeliveryStatus detras del dedupe por ID a secas.
func TestHandleOutbound_DistinctStatusesOfSameMessageStillCount(t *testing.T) {
	tracker := &mockDeliveryTracker{}
	h := newStatusHandler(tracker)

	h.handleOutbound(statusEvent("msg-life-1", "accepted"))
	h.handleOutbound(statusEvent("msg-life-1", "delivery_failed"))
	h.handleOutbound(statusEvent("msg-life-1", "delivered")) // recuperado: resetea el contador

	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	if len(tracker.failures) != 1 {
		t.Errorf("el delivery_failed del ciclo de vida debe contar una vez, conto %d (%v)", len(tracker.failures), tracker.failures)
	}
	if len(tracker.successes) != 1 {
		t.Errorf("el delivered posterior debe resetear el contador, se registro %d veces (%v)", len(tracker.successes), tracker.successes)
	}
}

// Y fallos de MENSAJES distintos siguen acumulando: es el caso que la supresion existe para cazar.
func TestHandleOutbound_FailuresOfDifferentMessagesAccumulate(t *testing.T) {
	tracker := &mockDeliveryTracker{}
	h := newStatusHandler(tracker)

	h.handleOutbound(statusEvent("msg-a", "delivery_failed"))
	h.handleOutbound(statusEvent("msg-b", "delivery_failed"))

	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	if len(tracker.failures) != 2 {
		t.Errorf("dos mensajes distintos fallidos deben contar 2, conto %d (%v)", len(tracker.failures), tracker.failures)
	}
}
