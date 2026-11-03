package worker

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/nectar"
	"github.com/neuro-bot/neuro-bot/internal/session"
)

// El reintento de retrySend sobre una conversación de Néctar, con el cliente real: un 504 de un
// proxy deja el envío EN DUDA y el reintento tiene que llevar la MISMA idempotency_key, para que
// Néctar lo deduplique en vez de mandarle el mensaje dos veces al paciente.
func TestRetrySend_NectarInDoubtReusesIdempotencyKey(t *testing.T) {
	var mu sync.Mutex
	var keys []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Key string `json:"idempotency_key"`
		}
		_ = json.Unmarshal(body, &req)
		mu.Lock()
		keys = append(keys, req.Key)
		first := len(keys) == 1
		mu.Unlock()
		if first {
			w.WriteHeader(http.StatusGatewayTimeout)
			return
		}
		_, _ = w.Write([]byte(`{"id":55}`))
	}))
	defer srv.Close()

	bc := bird.NewClientForTest(srv.URL)
	bc.SetNectar(nectar.NewBridge(nectar.NewClient(srv.URL, "clave", "secreto"), nil))
	pool := retryTestPool(&mockMessageSender{}, &mockSessionMgmt{})
	pool.birdClient = bc
	sess := &session.Session{ID: "s-nectar", PhoneNumber: nectar.SessionKey(1234)}

	pool.sendAndSave(context.Background(), sess, sess.PhoneNumber, textResult())

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(keys)
		mu.Unlock()
		if n >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(keys) != 2 || keys[0] == "" || keys[0] != keys[1] {
		t.Fatalf("el reintento del envío en duda tiene que reutilizar la clave: %v", keys)
	}
}
