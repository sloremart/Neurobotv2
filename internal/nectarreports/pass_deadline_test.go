package nectarreports

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// hungCycles cuelga la lectura del estado hasta que se cancele su ctx (MySQL que no responde).
type hungCycles struct {
	*fakeCycles
	calls atomic.Int32
}

func (h *hungCycles) GetState(ctx context.Context, _ string) (string, error) {
	h.calls.Add(1)
	<-ctx.Done()
	return "", ctx.Err()
}

// D-07: con MySQL colgado la pasada termina al vencer su plazo, deja UN error por pasada y la
// siguiente arranca.
func TestRun_HungStoreEndsPassAtDeadline(t *testing.T) {
	logs := captureLogs(t)
	c := &hungCycles{fakeCycles: newFakeCycles()}
	s := NewSyncer(&fakeReader{}, c, &fakeEscalations{}, time.Millisecond, DataFloor)
	s.now = func() time.Time { return DataFloor.Add(time.Hour) }
	s.passTimeout = 20 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	deadline := time.After(2 * time.Second)
	for c.calls.Load() < 3 {
		select {
		case <-deadline:
			cancel()
			t.Fatal("la pasada colgada no venció: la siguiente no arrancó")
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	<-done
	out := logs.String()
	if strings.Contains(out, "level=WARN") {
		t.Errorf("una pasada vencida es un ERROR, no un aviso: %q", out)
	}
	if n := strings.Count(out, "level=ERROR"); n < 2 {
		t.Errorf("esperaba un ERROR por pasada vencida (≥2), got %d: %q", n, out)
	}
}
