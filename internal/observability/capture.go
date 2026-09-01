package observability

import (
	"context"
	"sync"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/domain"
)

// CaptureSink acumula en memoria los eventos emitidos. Existe para que las pruebas de CUALQUIER
// paquete puedan afirmar qué quedó instrumentado y con qué atributos — no solo que el código no
// revienta. No se usa en producción: el sink real escribe en flow_events.
type CaptureSink struct {
	mu     sync.Mutex
	events []domain.FlowEvent
}

// InsertBatch satisface FlowSink acumulando el lote en memoria.
func (s *CaptureSink) InsertBatch(_ context.Context, evs []domain.FlowEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, evs...)
	return nil
}

// Events devuelve una copia de lo capturado hasta el momento.
func (s *CaptureSink) Events() []domain.FlowEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.FlowEvent, len(s.events))
	copy(out, s.events)
	return out
}

// FindStep devuelve el primer evento capturado de ese paso, o nil.
func (s *CaptureSink) FindStep(flow, step string) *domain.FlowEvent {
	for _, e := range s.Events() {
		if e.Flow == flow && e.Step == step {
			ev := e
			return &ev
		}
	}
	return nil
}

// CountStep cuenta los eventos capturados de ese paso.
func (s *CaptureSink) CountStep(flow, step string) int {
	n := 0
	for _, e := range s.Events() {
		if e.Flow == flow && e.Step == step {
			n++
		}
	}
	return n
}

// StartCapture instala un tracer que escribe en sink y devuelve stop(), que DRENA el buffer de
// forma determinista (sin esperar al ticker de flush) y restaura el tracer anterior. Pensado para
// pruebas: `defer observability.StartCapture(sink)()`.
func StartCapture(sink FlowSink) (stop func()) {
	prev := std.Load()
	tr := New(sink, LvMilestone)
	// G118: el cancel NO se pierde — lo invoca el stop() que se devuelve, que es justo el contrato
	// de este helper (arrancar aquí, drenar allí). gosec no puede seguir el cierre.
	ctx, cancel := context.WithCancel(context.Background()) //nolint:gosec // cancel se llama en stop()
	done := make(chan struct{})
	go func() { defer close(done); tr.Start(ctx) }()
	Init(tr)
	return func() {
		defer cancel() // G118: el cancel vive hasta que stop() drena; aquí se libera siempre
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second): // el drain no debe colgar una suite
		}
		std.Store(prev)
	}
}
