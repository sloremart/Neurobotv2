package worker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/monitor"
	"github.com/neuro-bot/neuro-bot/internal/nectar"
	"github.com/neuro-bot/neuro-bot/internal/session"
	"github.com/neuro-bot/neuro-bot/internal/statemachine"
)

// slowNectar es un falso Néctar que tarda `delay` en contestar 201 cada mensaje y anota las claves.
func slowNectar(t *testing.T, delay time.Duration) (*MessageWorkerPool, *session.Session, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var keys []string
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Key string `json:"idempotency_key"`
		}
		_ = json.Unmarshal(body, &req)
		mu.Lock()
		keys = append(keys, req.Key)
		mu.Unlock()
		select {
		case <-time.After(delay):
		case <-release:
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":55}`))
	}))
	t.Cleanup(func() { close(release); srv.Close() })

	bc := bird.NewClientForTest(srv.URL)
	bc.SetNectar(nectar.NewBridge(nectar.NewClient(srv.URL, "clave", "secreto"), nil))
	pool := retryTestPool(&mockMessageSender{}, &mockSessionMgmt{})
	pool.birdClient = bc
	pool.minSendBudget = 50 * time.Millisecond
	return pool, &session.Session{ID: "s-nectar", PhoneNumber: nectar.SessionKey(1234)}, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), keys...)
	}
}

// B-07: con Néctar lenta, sendAndSave vuelve al vencer el plazo del mensaje; lo abandonado tiene su primer
// reintento fuera del turno, con la misma clave en duda (I-2).
func TestSendAndSave_NectarSlow_RespectsMessageDeadline(t *testing.T) {
	for name, msg := range map[string]statemachine.OutboundMessage{
		"texto":   &statemachine.TextMessage{Text: "hola"},
		"botones": &statemachine.ButtonMessage{Text: "elige", Buttons: []statemachine.Button{{Text: "a", Payload: "a"}}},
		"lista": &statemachine.ListMessage{Body: "elige", Title: "Ver", Sections: []statemachine.ListSection{
			{Title: "s", Rows: []statemachine.ListRow{{ID: "1", Title: "uno"}}},
		}},
	} {
		t.Run(name, func(t *testing.T) {
			pool, sess, keys := slowNectar(t, 10*time.Second)
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()

			start := time.Now()
			pool.sendAndSave(ctx, sess, sess.PhoneNumber, &statemachine.StateResult{
				NextState: "MAIN_MENU", Messages: []statemachine.OutboundMessage{msg},
			})
			if d := time.Since(start); d > 2*time.Second {
				t.Fatalf("sendAndSave tardó %v con un plazo de 200 ms", d)
			}
			deadline := time.Now().Add(2 * time.Second)
			for time.Now().Before(deadline) && len(keys()) < 2 {
				time.Sleep(5 * time.Millisecond)
			}
			if got := keys(); len(got) != 2 || got[0] != got[1] {
				t.Fatalf("el reintento de lo abandonado lleva la misma clave: %v", got)
			}
		})
	}
}

// I-2: un envío abandonado por el plazo del turno tiene su primer reintento, como antes del lote, y lo
// entrega aunque el contexto del turno ya venció: corre con el de la aplicación.
func TestSendAndSave_AbandonedSendGetsFirstRetry(t *testing.T) {
	for name, first := range map[string]func(w http.ResponseWriter, release <-chan struct{}){
		"502 lento": func(w http.ResponseWriter, _ <-chan struct{}) {
			time.Sleep(300 * time.Millisecond)
			w.WriteHeader(http.StatusBadGateway)
		},
		"cuelgue": func(_ http.ResponseWriter, release <-chan struct{}) { <-release },
	} {
		t.Run(name, func(t *testing.T) {
			var mu sync.Mutex
			var keys []string
			delivered := 0
			release := make(chan struct{})
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					Key string `json:"idempotency_key"`
				}
				_ = json.NewDecoder(r.Body).Decode(&req)
				mu.Lock()
				keys = append(keys, req.Key)
				isFirst := len(keys) == 1
				mu.Unlock()
				if isFirst {
					first(w, release)
					return
				}
				mu.Lock()
				delivered++
				mu.Unlock()
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{"id":55}`))
			}))
			t.Cleanup(func() { close(release); srv.Close() })
			bc := bird.NewClientForTest(srv.URL)
			bc.SetNectar(nectar.NewBridge(nectar.NewClient(srv.URL, "clave", "secreto"), nil))
			pool := retryTestPool(&mockMessageSender{}, &mockSessionMgmt{})
			pool.birdClient = bc
			pool.minSendBudget = 50 * time.Millisecond
			sess := &session.Session{ID: "s-nectar", PhoneNumber: nectar.SessionKey(1234)}
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()

			pool.sendAndSave(ctx, sess, sess.PhoneNumber, textResult())

			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				mu.Lock()
				done := delivered == 1
				mu.Unlock()
				if done {
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			mu.Lock()
			defer mu.Unlock()
			if delivered != 1 || len(keys) != 2 || keys[0] != keys[1] {
				t.Fatalf("entregados = %d, claves = %v", delivered, keys)
			}
		})
	}
}

// El primer reintento también sale si ya empezó un turno posterior: el abandono no le quita esa regla.
func TestSendAndSave_AbandonedFirstRetryHasNoGuard(t *testing.T) {
	pool, sess, keys := slowNectar(t, 10*time.Second)
	pool.retryDelay = 100 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	pool.sendAndSave(ctx, sess, sess.PhoneNumber, textResult())
	pool.beginTurn(sess.PhoneNumber)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && len(keys()) < 2 {
		time.Sleep(5 * time.Millisecond)
	}
	if got := keys(); len(got) != 2 {
		t.Fatalf("intentos = %d, quiero 2", len(got))
	}
}

// B-02: lo abandonado puede haber salido (Néctar contesta 2xx cuando el bot ya se fue): un envío igual
// posterior lleva la misma clave y Néctar no lo manda dos veces.
func TestSendAndSave_AbandonedSendLeavesKeyInDoubt(t *testing.T) {
	pool, sess, keys := slowNectar(t, 300*time.Millisecond)
	pool.retryDelay = time.Minute // el envío igual llega antes que el reintento
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	pool.sendAndSave(ctx, sess, sess.PhoneNumber, textResult())
	pool.sendAndSave(context.Background(), sess, sess.PhoneNumber, textResult())

	got := keys()
	if len(got) != 2 || got[0] == "" || got[0] != got[1] {
		t.Fatalf("claves = %v", got)
	}
	pool.Stop()
}

// Un turno que ya agotó su plazo manda igual su respuesta si Néctar contesta rápido.
func TestSendAndSave_ExpiredTurnStillSends(t *testing.T) {
	pool, sess, keys := slowNectar(t, 0)
	pool.minSendBudget = 2 * time.Second
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	pool.sendAndSave(ctx, sess, sess.PhoneNumber, textResult())

	time.Sleep(5 * pool.retryDelay)
	if got := keys(); len(got) != 1 {
		t.Fatalf("intentos = %d, quiero 1", len(got))
	}
}

// Con plazo de sobra manda el del turno; con poco, el mínimo.
func TestSendDeadline_KeepsTurnDeadlineAndFloor(t *testing.T) {
	pool := retryTestPool(&mockMessageSender{}, &mockSessionMgmt{})
	pool.minSendBudget = time.Minute

	turnDeadline := time.Now().Add(90 * time.Second)
	ctx, cancel := context.WithDeadline(context.Background(), turnDeadline)
	defer cancel()
	if d := pool.sendDeadline(ctx); !d.Equal(turnDeadline) {
		t.Errorf("con plazo de sobra manda el del turno: %v", d)
	}

	short, shortCancel := context.WithTimeout(context.Background(), time.Second)
	defer shortCancel()
	if d := pool.sendDeadline(short); time.Until(d) < 50*time.Second {
		t.Errorf("con poco plazo manda el mínimo: %v", time.Until(d))
	}
}

// Un turno cancelado (no vencido) también responde: el envío no hereda la cancelación.
func TestSendAndSave_CanceledTurnStillSends(t *testing.T) {
	pool, sess, keys := slowNectar(t, 0)
	pool.minSendBudget = 2 * time.Second
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	pool.sendAndSave(ctx, sess, sess.PhoneNumber, textResult())

	time.Sleep(5 * pool.retryDelay)
	if got := keys(); len(got) != 1 {
		t.Fatalf("intentos = %d, quiero 1", len(got))
	}
}

// Bird no toma el plazo: aunque venza, conserva su reintento.
func TestSendAndSave_BirdRetriesEvenIfDeadlinePassed(t *testing.T) {
	sender := &mockMessageSender{sendErr: errors.New("dial tcp: connection refused")}
	pool := retryTestPool(sender, &mockSessionMgmt{})
	pool.minSendBudget = time.Nanosecond
	sess := &session.Session{ID: "s-bird", PhoneNumber: "+573001234567"}
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	pool.sendAndSave(ctx, sess, sess.PhoneNumber, textResult())

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && countSent(sender, "text") < 2 {
		time.Sleep(5 * time.Millisecond)
	}
	if got := countSent(sender, "text"); got != 2 {
		t.Fatalf("Bird conserva su reintento: %d envíos", got)
	}
}

// downCounter cuenta lo que el cliente de Néctar reporta para el aviso urgente.
type downCounter struct{ n atomic.Int32 }

func (d *downCounter) Count(context.Context, monitor.UrgentTrigger, int, time.Duration, string) {
	d.n.Add(1)
}

// M-3 con el pool real: el abandono por el plazo del turno no cuenta para nectar_down.
func TestSendAndSave_AbandonedSendDoesNotCountAsNectarDown(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	t.Cleanup(func() { close(release); srv.Close() })
	client := nectar.NewClient(srv.URL, "clave", "secreto")
	counter := &downCounter{}
	client.SetUrgentReporter(counter, nectar.UrgentLimits{DownCount: 5, DownWindow: time.Minute})
	bc := bird.NewClientForTest(srv.URL)
	bc.SetNectar(nectar.NewBridge(client, nil))
	pool := retryTestPool(&mockMessageSender{}, &mockSessionMgmt{})
	pool.birdClient = bc
	pool.minSendBudget = 50 * time.Millisecond
	pool.retryDelay = 5 * time.Second // solo interesa el envío abandonado
	sess := &session.Session{ID: "s-nectar", PhoneNumber: nectar.SessionKey(1234)}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	pool.sendAndSave(ctx, sess, sess.PhoneNumber, textResult())

	if n := counter.n.Load(); n != 0 {
		t.Fatalf("el abandono contó %d como Néctar caída", n)
	}
}
