package worker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/nectar"
	"github.com/neuro-bot/neuro-bot/internal/observability"
	"github.com/neuro-bot/neuro-bot/internal/session"
	"github.com/neuro-bot/neuro-bot/internal/statemachine"
)

// sendAttempt es una llegada al falso Néctar: su clave y cuándo llegó.
type sendAttempt struct {
	key string
	at  time.Time
}

// fakeNectarSend es un falso Néctar cuyo guion dice qué contesta cada POST de mensaje (el último paso
// se repite; un paso 2xx entrega).
type fakeNectarSend struct {
	mu       sync.Mutex
	attempts []sendAttempt
	script   []func(w http.ResponseWriter)
}

func status(code int) func(w http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.WriteHeader(code)
		if code < 300 {
			_, _ = w.Write([]byte(`{"id":55}`))
		}
	}
}

func rateLimited(retryAfter string) func(w http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Retry-After", retryAfter)
		w.WriteHeader(http.StatusTooManyRequests)
	}
}

// newNectarRetryPool arma un pool con el cliente real contra el falso Néctar, y la sesión de esa conversación.
func newNectarRetryPool(t *testing.T, script ...func(w http.ResponseWriter)) (*MessageWorkerPool, *fakeNectarSend, *session.Session) {
	t.Helper()
	f := &fakeNectarSend{script: script}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Key string `json:"idempotency_key"`
		}
		_ = json.Unmarshal(body, &req)
		f.mu.Lock()
		f.attempts = append(f.attempts, sendAttempt{key: req.Key, at: time.Now()})
		step := f.script[min(len(f.attempts), len(f.script))-1]
		f.mu.Unlock()
		step(w)
	}))
	t.Cleanup(srv.Close)

	bc := bird.NewClientForTest(srv.URL)
	bc.SetNectar(nectar.NewBridge(nectar.NewClient(srv.URL, "clave", "secreto"), nil))
	pool := retryTestPool(&mockMessageSender{}, &mockSessionMgmt{})
	pool.birdClient = bc
	return pool, f, &session.Session{ID: "s-nectar", PhoneNumber: nectar.SessionKey(1234)}
}

func (f *fakeNectarSend) sent() []sendAttempt {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sendAttempt(nil), f.attempts...)
}

// waitAttempts espera a que lleguen n intentos y confirma que no llegan más.
func waitAttempts(t *testing.T, f *fakeNectarSend, n int, settle time.Duration) []sendAttempt {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && len(f.sent()) < n {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(settle)
	got := f.sent()
	if len(got) != n {
		t.Fatalf("intentos = %d, quiero %d", len(got), n)
	}
	return got
}

// D-06: 5xx y transporte se reintentan dos veces, a retryDelay y a 3×retryDelay, con la MISMA clave en
// duda, y ninguno más.
func TestRetrySend_Nectar5xxRetriesTwiceWithSameKey(t *testing.T) {
	pool, f, sess := newNectarRetryPool(t, status(http.StatusBadGateway))
	pool.retryDelay = 50 * time.Millisecond

	pool.sendAndSave(context.Background(), sess, sess.PhoneNumber, textResult())

	got := waitAttempts(t, f, 3, 300*time.Millisecond)
	if got[0].key == "" || got[1].key != got[0].key || got[2].key != got[0].key {
		t.Fatalf("todos los reintentos llevan la misma clave: %+v", got)
	}
	if d := got[1].at.Sub(got[0].at); d < pool.retryDelay {
		t.Errorf("primer reintento a %v, quiero ≥ %v", d, pool.retryDelay)
	}
	if d := got[2].at.Sub(got[1].at); d < 3*pool.retryDelay {
		t.Errorf("segundo reintento a %v, quiero ≥ %v", d, 3*pool.retryDelay)
	}
}

func TestRetrySend_NectarStopsOnSuccess(t *testing.T) {
	pool, f, sess := newNectarRetryPool(t, status(http.StatusServiceUnavailable), status(http.StatusOK))

	pool.sendAndSave(context.Background(), sess, sess.PhoneNumber, textResult())

	waitAttempts(t, f, 2, 200*time.Millisecond)
}

// Lo que no es 5xx, transporte ni 429 no se reintenta en Néctar, ni de primeras ni tras un reintento.
func TestRetrySend_NectarOnlyRetriesTransient(t *testing.T) {
	for name, code := range map[string]int{
		"400": http.StatusBadRequest,
		"403": http.StatusForbidden,
		"404": http.StatusNotFound,
		"409": http.StatusConflict,
	} {
		t.Run("de primeras "+name, func(t *testing.T) {
			pool, f, sess := newNectarRetryPool(t, status(code))
			pool.sendAndSave(context.Background(), sess, sess.PhoneNumber, textResult())
			waitAttempts(t, f, 1, 150*time.Millisecond)
		})
	}
	t.Run("tras un reintento", func(t *testing.T) {
		pool, f, sess := newNectarRetryPool(t, status(http.StatusBadGateway), status(http.StatusBadRequest))
		pool.sendAndSave(context.Background(), sess, sess.PhoneNumber, textResult())
		waitAttempts(t, f, 2, 200*time.Millisecond)
	})
}

// B-04: el 429 espera lo que pide su Retry-After si es más que el retraso normal.
func TestRetrySend_429HonoursRetryAfter(t *testing.T) {
	pool, f, sess := newNectarRetryPool(t, rateLimited("1"), status(http.StatusOK))

	pool.sendAndSave(context.Background(), sess, sess.PhoneNumber, textResult())

	got := waitAttempts(t, f, 2, 50*time.Millisecond)
	if d := got[1].at.Sub(got[0].at); d < 900*time.Millisecond {
		t.Fatalf("el reintento salió a %v, antes del Retry-After", d)
	}
	if got[1].key != got[0].key {
		t.Fatalf("misma clave: %+v", got)
	}
}

// B-04: si pide más de lo que el bot espera, no se reintenta y queda el evento propio.
func TestRetrySend_429RetryAfterTooLong(t *testing.T) {
	sink := &observability.CaptureSink{}
	stop := observability.StartCapture(sink)
	pool, f, sess := newNectarRetryPool(t, rateLimited("61"))

	pool.sendAndSave(context.Background(), sess, sess.PhoneNumber, textResult())
	waitAttempts(t, f, 1, 150*time.Millisecond)
	stop()

	e := sink.FindStep("infra", "send_retry_after_too_long")
	if e == nil {
		t.Fatalf("falta el evento: %+v", sink.Events())
	}
	if e.TraceID != observability.TraceSession(sess.ID) || e.Outcome != "error" {
		t.Errorf("evento = %+v", e)
	}
}

// 60 s justos todavía se respetan: la decisión, sin dormir el minuto.
func TestSendRetryWait_RetryAfterLimit(t *testing.T) {
	pool, f, sess := newNectarRetryPool(t, rateLimited("60"))
	_, err := pool.sendMessage(sess.PhoneNumber, "", &statemachine.TextMessage{Text: "hola"})
	if err == nil || len(f.sent()) != 1 {
		t.Fatalf("err = %v", err)
	}
	if wait, ok := pool.sendRetryWait(sess.ID, sess.PhoneNumber, 1, err); !ok || wait != maxSendRetryAfter {
		t.Fatalf("wait = %v, ok = %v", wait, ok)
	}
}

// Los reintentos extra no corren si no caben en el plazo del mensaje; el de siempre sí.
func TestRetrySend_ExtraRetryMustFitDeadline(t *testing.T) {
	pool, f, sess := newNectarRetryPool(t, status(http.StatusBadGateway))
	ctx, cancel := context.WithTimeout(context.Background(), sendAttemptBudget/2)
	defer cancel()

	pool.sendAndSave(ctx, sess, sess.PhoneNumber, textResult())

	waitAttempts(t, f, 2, 200*time.Millisecond)
}

// Lo mismo con el 429: esperar su Retry-After no cabe, y no se reintenta antes de lo que pidió.
func TestRetrySend_LongRetryAfterMustFitDeadline(t *testing.T) {
	pool, f, sess := newNectarRetryPool(t, rateLimited("30"))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool.sendAndSave(ctx, sess, sess.PhoneNumber, textResult())

	waitAttempts(t, f, 1, 150*time.Millisecond)
}

// El orden: si ya salió un mensaje posterior a ese paciente, el reintento extra no corre. El primero
// sí, como siempre: no llega más tarde de lo que ya llegaba.
func TestRetrySend_ExtraRetrySkippedAfterLaterMessage(t *testing.T) {
	pool, f, sess := newNectarRetryPool(t, status(http.StatusBadGateway), status(http.StatusOK), status(http.StatusBadGateway))
	pool.retryDelay = 400 * time.Millisecond // más que los 300 ms entre mensajes del turno

	// Falla el primero; el segundo mensaje, posterior, sale; el primer reintento del primero falla.
	pool.sendAndSave(context.Background(), sess, sess.PhoneNumber, &statemachine.StateResult{
		NextState: "MAIN_MENU",
		Messages: []statemachine.OutboundMessage{
			&statemachine.ButtonMessage{Text: "elige", Buttons: []statemachine.Button{{Text: "a", Payload: "a"}}},
			&statemachine.TextMessage{Text: "posterior"},
		},
	})

	got := waitAttempts(t, f, 3, 3*pool.retryDelay+200*time.Millisecond)
	if got[2].key != got[0].key {
		t.Fatalf("el tercer intento es el reintento del primero: %+v", got)
	}
}

// Un segundo reintento sin mensajes posteriores sí corre aunque ya haya salido uno ANTES del fallo.
func TestSentNectarAfter_OnlyCountsLaterMessages(t *testing.T) {
	pool := retryTestPool(&mockMessageSender{}, &mockSessionMgmt{})
	now := time.Now()
	if pool.sentNectarAfter("nectar:1", now) {
		t.Fatal("sin envíos")
	}
	pool.lastNectarSend.Store("nectar:1", now.Add(-time.Second))
	if pool.sentNectarAfter("nectar:1", now) {
		t.Fatal("el envío es anterior")
	}
	pool.lastNectarSend.Store("nectar:1", now.Add(time.Second))
	if !pool.sentNectarAfter("nectar:1", now) || pool.sentNectarAfter("nectar:2", now) {
		t.Fatal("el envío es posterior, y solo de ese paciente")
	}
	pool.purgeStaleLockMisses(now.Add(2*processMsgTimeout + 2*time.Second))
	if _, ok := pool.lastNectarSend.Load("nectar:1"); ok {
		t.Fatal("la limpieza purga las entradas viejas")
	}
}

// Bird cobra cada POST: sigue con su único reintento, sea cual sea el fallo.
func TestRetrySend_BirdKeepsSingleRetry(t *testing.T) {
	sender := &mockMessageSender{sendErr: errors.New("bird api after 3 attempts: 502")}
	pool := retryTestPool(sender, &mockSessionMgmt{})
	sess := &session.Session{ID: "s-bird", PhoneNumber: "+573001234567"}

	pool.sendAndSave(context.Background(), sess, sess.PhoneNumber, textResult())

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && countSent(sender, "text") < 2 {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(5 * pool.retryDelay)
	if got := countSent(sender, "text"); got != 2 {
		t.Fatalf("Bird hace el envío y un reintento: %d envíos", got)
	}
}

// El turno no espera a los reintentos: el candado del teléfono se libera al volver sendAndSave.
func TestSendAndSave_DoesNotWaitForRetries(t *testing.T) {
	pool, f, sess := newNectarRetryPool(t, status(http.StatusBadGateway))
	pool.retryDelay = time.Second

	start := time.Now()
	pool.sendAndSave(context.Background(), sess, sess.PhoneNumber, textResult())
	if d := time.Since(start); d >= pool.retryDelay {
		t.Fatalf("sendAndSave tardó %v: esperó el reintento", d)
	}
	if n := len(f.sent()); n != 1 {
		t.Fatalf("intentos dentro del turno = %d", n)
	}
	pool.Stop()
}
