package observability

import (
	"context"
	"sync"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/domain"
)

type fakeSink struct {
	mu     sync.Mutex
	events []domain.FlowEvent
}

func (f *fakeSink) InsertBatch(_ context.Context, evs []domain.FlowEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, evs...)
	return nil
}

func (f *fakeSink) all() []domain.FlowEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]domain.FlowEvent(nil), f.events...)
}

// runEmits corre un Tracer al nivel dado, ejecuta los emits y drena en el apagado (determinista).
func runEmits(maxLvl Level, emits func(tr *Tracer)) []domain.FlowEvent {
	sink := &fakeSink{}
	tr := New(sink, maxLvl)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		tr.Start(ctx)
		close(done)
	}()
	emits(tr)
	cancel()
	<-done
	return sink.all()
}

// TestTracer_LevelGate verifica que FLOW_TRACE_LEVEL filtra acumulativamente.
func TestTracer_LevelGate(t *testing.T) {
	do := func(tr *Tracer) {
		tr.emit("wl:1", "lista_espera", "notify_failed", EmitOpts{}) // error (1)
		tr.emit("wl:1", "lista_espera", "booked", EmitOpts{})        // outcome (2)
		tr.emit("wl:1", "lista_espera", "slot_match", EmitOpts{})    // milestone (3)
		tr.emit("wl:1", "lista_espera", "skipped", EmitOpts{})       // full (4)
	}
	cases := []struct {
		lvl  Level
		want int
	}{
		{LvOff, 0}, {LvError, 1}, {LvOutcome, 2}, {LvMilestone, 3}, {LvFull, 4},
	}
	for _, c := range cases {
		if got := runEmits(c.lvl, do); len(got) != c.want {
			t.Errorf("level %d: got %d events, want %d", c.lvl, len(got), c.want)
		}
	}
}

// TestTracer_WaitingListJourneyOrdered es el criterio de aceptación de la Fase 0: el recorrido
// completo de una lista de espera queda registrado, ordenado y bajo el mismo trace_id.
func TestTracer_WaitingListJourneyOrdered(t *testing.T) {
	got := runEmits(LvMilestone, func(tr *Tracer) {
		tr.emit("wl:7", "lista_espera", "enrolled", EmitOpts{Phone: "+573001234567"})
		tr.emit("wl:7", "lista_espera", "slot_match", EmitOpts{})
		tr.emit("wl:7", "lista_espera", "notified", EmitOpts{RefID: "bird-1"})
		tr.emit("wl:7", "lista_espera", "response_schedule", EmitOpts{})
		tr.emit("wl:7", "lista_espera", "booked", EmitOpts{RefID: "cita-9"})
	})
	want := []string{"enrolled", "slot_match", "notified", "response_schedule", "booked"}
	if len(got) != len(want) {
		t.Fatalf("got %d events, want %d", len(got), len(want))
	}
	for i, s := range want {
		if got[i].Step != s {
			t.Errorf("event %d: got %q want %q", i, got[i].Step, s)
		}
		if got[i].TraceID != "wl:7" {
			t.Errorf("event %d trace_id = %q, want wl:7", i, got[i].TraceID)
		}
	}
	// ref_id de booked debe ser la cita (pivote entre trazas)
	if got[4].RefID != "cita-9" || got[4].RefType != "cita" {
		t.Errorf("booked ref = %q/%q, want cita-9/cita", got[4].RefID, got[4].RefType)
	}
}

func TestTracer_MasksPhone(t *testing.T) {
	got := runEmits(LvMilestone, func(tr *Tracer) {
		tr.emit("wl:1", "lista_espera", "notified", EmitOpts{Phone: "+573001234567"})
	})
	if len(got) != 1 {
		t.Fatalf("want 1 event, got %d", len(got))
	}
	if got[0].Phone == "+573001234567" || got[0].Phone == "" {
		t.Errorf("phone not masked: %q", got[0].Phone)
	}
}

func TestSanitizeAttrs_DropsPII(t *testing.T) {
	out := sanitizeAttrs(map[string]interface{}{
		"cups": "890274", "espacios": 2, "nombre": "Juan", "documento": "123", "direccion": "calle 1",
	})
	if _, ok := out["cups"]; !ok {
		t.Error("cups (permitida) debió pasar")
	}
	if _, ok := out["espacios"]; !ok {
		t.Error("espacios (permitida) debió pasar")
	}
	for _, bad := range []string{"nombre", "documento", "direccion"} {
		if _, ok := out[bad]; ok {
			t.Errorf("%q (PII) NO debió pasar", bad)
		}
	}
}

func TestParseLevel(t *testing.T) {
	cases := map[string]Level{
		"off": LvOff, "error": LvError, "outcome": LvOutcome,
		"milestone": LvMilestone, "full": LvFull, "": LvMilestone, "basura": LvMilestone,
	}
	for in, want := range cases {
		if got := ParseLevel(in); got != want {
			t.Errorf("ParseLevel(%q) = %d, want %d", in, got, want)
		}
	}
}

// TestCatalog_WaitingListRegistered: todos los steps que la lista de espera emite tienen entrada.
func TestCatalog_WaitingListRegistered(t *testing.T) {
	steps := []string{
		"enrolled", "slot_match", "skipped", "claim_lost", "duplicate_found",
		"notified", "notify_failed", "response_schedule", "declined", "booked", "expired",
	}
	for _, s := range steps {
		if _, ok := catalog["lista_espera/"+s]; !ok {
			t.Errorf("catalog sin entrada para lista_espera/%s", s)
		}
	}
}
