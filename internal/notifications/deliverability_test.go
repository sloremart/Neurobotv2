package notifications

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/config"
)

// Supresión por fallo de entrega: el webhook de Bird registra los envíos que WhatsApp no pudo
// entregar (número sin WA). Tras 2 fallos CONSECUTIVOS confirmados, los templates programados a
// ese número se suprimen — cada uno se cobraba sin llegar jamás. Fail-open ante cualquier duda.

type mockDeliverability struct {
	failures int
	err      error
}

func (m *mockDeliverability) ConsecutiveFailures(_ context.Context, _ string) (int, error) {
	return m.failures, m.err
}

func TestDeliverable_FailOpenWithoutChecker(t *testing.T) {
	mgr := NewNotificationManager(nil, nil, &config.Config{})
	if !mgr.Deliverable(context.Background(), "+573001234567") {
		t.Error("sin checker debe ser fail-open (true)")
	}
}

func TestDeliverable_FailOpenOnError(t *testing.T) {
	mgr := NewNotificationManager(nil, nil, &config.Config{})
	mgr.SetDeliverability(&mockDeliverability{err: errors.New("db down")})
	if !mgr.Deliverable(context.Background(), "+573001234567") {
		t.Error("un error de BD no debe suprimir envíos (fail-open)")
	}
}

func TestDeliverable_ThresholdBehavior(t *testing.T) {
	mgr := NewNotificationManager(nil, nil, &config.Config{})

	mgr.SetDeliverability(&mockDeliverability{failures: 1})
	if !mgr.Deliverable(context.Background(), "+573001234567") {
		t.Error("1 fallo no alcanza el umbral: debe seguir enviando")
	}

	mgr.SetDeliverability(&mockDeliverability{failures: 2})
	if mgr.Deliverable(context.Background(), "+573001234567") {
		t.Error("2 fallos consecutivos = número sin WhatsApp: suprimir")
	}
}

// Lista de espera: entrada suprimida no llama a Bird y queda 'unreachable' (fuera del pool diario).
func TestCheckWaitingListForCups_Suppressed_MarksUnreachableWithoutHTTP(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"id":"x"}`))
	}))
	defer srv.Close()

	mgr, wlChecker := wlTestManager(t, bird.NewClientForTest(srv.URL), wlEntry("+573005551234"))
	mgr.SetDeliverability(&mockDeliverability{failures: 3})

	mgr.CheckWaitingListForCups(context.Background(), "890271")

	if hits.Load() != 0 {
		t.Errorf("número suprimido: no debe haber llamada HTTP, hubo %d", hits.Load())
	}
	wlChecker.mu.Lock()
	defer wlChecker.mu.Unlock()
	if wlChecker.updatedStatus != "unreachable" {
		t.Errorf("esperaba status 'unreachable', got %q", wlChecker.updatedStatus)
	}
}
