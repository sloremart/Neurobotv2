// Package api: tests de la agregacion de llamadas por agente (F3 voz).
package api

import (
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/bird"
)

func call(created, from, to, status string, dur int) bird.CallRecord {
	return bird.CallRecord{CreatedAt: created, From: from, To: to, Status: status, Duration: dur}
}

// La agregacion por agente: pierna del agente como unidad, sin doble-cuenta de la llamada puenteada,
// no-answer = el agente no contesto, ventana respetada.
func TestAggregateAgentCalls(t *testing.T) {
	from, _ := time.Parse(time.RFC3339, "2026-07-20T00:00:00Z")
	to, _ := time.Parse(time.RFC3339, "2026-07-22T23:59:59Z")
	calls := []bird.CallRecord{
		// Llamada puenteada saliente del agente A: pierna webrtc (cuenta) + pierna pstn (NO cuenta).
		call("2026-07-21T10:00:00Z", "client:A", "+573001", "completed", 120),
		call("2026-07-21T10:00:00Z", "+573009", "+573001", "completed", 120),
		// Entrante ofrecida al agente A: contesta.
		call("2026-07-21T11:00:00Z", "+573002", "client:A", "completed", 60),
		// Ofrecida al agente A: NO contesta.
		call("2026-07-21T12:00:00Z", "+573003", "client:A", "no-answer", 0),
		// Ofrecida al agente B: colgada antes de contestar.
		call("2026-07-21T13:00:00Z", "+573004", "client:B", "cancelled", 0),
		// En curso: se excluye.
		call("2026-07-21T14:00:00Z", "+573005", "client:A", "ongoing", 999),
		// Fuera de ventana: se excluye.
		call("2026-07-10T10:00:00Z", "+573006", "client:A", "no-answer", 0),
	}
	agg, anyIn := aggregateAgentCalls(calls, from, to)
	if !anyIn {
		t.Fatal("habia llamadas en ventana")
	}
	a := agg["A"]
	if a == nil || a.Made != 1 || a.Offered != 2 || a.Answered != 1 || a.NoAnswer != 1 || a.TotalSeconds != 180 {
		t.Errorf("agente A mal: %+v", a)
	}
	b := agg["B"]
	if b == nil || b.Offered != 1 || b.Cancelled != 1 || b.Answered != 0 {
		t.Errorf("agente B mal: %+v", b)
	}
	// Ventana vacia → anyIn false.
	_, none := aggregateAgentCalls(calls[:1], to.Add(24*time.Hour), to.Add(48*time.Hour))
	if none {
		t.Error("fuera de ventana debia dar anyIn=false")
	}
}
