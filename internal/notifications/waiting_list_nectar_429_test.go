package notifications

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/nectar"
)

// Un 429 de Néctar en la apertura de la oferta es pasajero: la entrada vuelve a 'waiting' para el
// siguiente chequeo, igual que el 429 de Bird. Quedar 'unreachable' la sacaría de la lista para
// siempre (ninguna consulta del pool rescata ese estado).
func TestCheckWaitingListForCups_Nectar429_RevertsToWaiting(t *testing.T) {
	var opens atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/bot/conversations/open" {
			opens.Add(1)
		}
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	bc := bird.NewClientForTest(srv.URL)
	br := nectar.NewBridge(nectar.NewClient(srv.URL, "clave", "secreto"), map[string]string{"proj-wl-123": "lista_espera"})
	br.ChannelID = 7
	bc.SetNectar(br)
	mgr, wlChecker := wlTestManager(t, bc, wlEntry("+573005551234"))

	if count := mgr.CheckWaitingListForCups(context.Background(), "890271"); count != 0 {
		t.Errorf("no debió contar notificados, got %d", count)
	}
	if opens.Load() != 1 {
		t.Fatalf("una apertura y sin reintento (no trae Retry-After): %d", opens.Load())
	}
	wlChecker.mu.Lock()
	defer wlChecker.mu.Unlock()
	if wlChecker.updatedStatus != "waiting" {
		t.Errorf("esperaba revert a 'waiting', got %q", wlChecker.updatedStatus)
	}
}
