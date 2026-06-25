package observability

import (
	"context"
	"errors"
	"testing"
)

// TestReconciler_EmitsAnomalies: un check que devuelve infracciones emite anomalías (flow=invariante,
// level=error) y un check que falla NO rompe a los demás.
func TestReconciler_EmitsAnomalies(t *testing.T) {
	sink := &fakeSink{}
	tr := New(sink, LvError) // la reconciliación usa el Emit package-level
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		tr.Start(ctx)
		close(done)
	}()
	Init(tr)
	defer Init(nil)

	rec := NewReconciler()
	rec.Register("orphan_slot", func(_ context.Context) ([]Violation, error) {
		return []Violation{
			{TraceID: "invariante:orphan_slot", Reason: "orphan_slot", RefType: "cita", RefID: "10"},
			{TraceID: "invariante:orphan_slot", Reason: "orphan_slot", RefType: "cita", RefID: "11"},
		}, nil
	})
	rec.Register("boom", func(_ context.Context) ([]Violation, error) {
		return nil, errors.New("db down")
	})
	rec.Run(context.Background())

	cancel()
	<-done

	got := sink.all()
	if len(got) != 2 {
		t.Fatalf("got %d anomalies, want 2", len(got))
	}
	for _, e := range got {
		if e.Flow != "invariante" || e.Step != "anomaly" || e.Outcome != "error" {
			t.Errorf("anomalía mal formada: flow=%s step=%s outcome=%s", e.Flow, e.Step, e.Outcome)
		}
		if e.Level != int(LvError) {
			t.Errorf("anomalía level=%d, want %d (error)", e.Level, int(LvError))
		}
		if e.RefType != "cita" { // override de EmitOpts.RefType
			t.Errorf("refType=%s, want cita", e.RefType)
		}
		if e.Reason != "orphan_slot" {
			t.Errorf("reason=%s, want orphan_slot", e.Reason)
		}
	}
}
