package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/nectar"
	"github.com/neuro-bot/neuro-bot/internal/observability"
	"github.com/neuro-bot/neuro-bot/internal/session"
	"github.com/neuro-bot/neuro-bot/internal/statemachine"
)

// sendAttempt es una llegada al falso Néctar: su clave, su texto, cuándo llegó y qué se le contestó.
type sendAttempt struct {
	key    string
	text   string
	at     time.Time
	status int
}

// codeRecorder anota el código con el que contesta un paso del guion.
type codeRecorder struct {
	http.ResponseWriter
	code int
}

func (r *codeRecorder) WriteHeader(code int) {
	r.code = code
	r.ResponseWriter.WriteHeader(code)
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
			Key  string `json:"idempotency_key"`
			Text string `json:"text"`
		}
		_ = json.Unmarshal(body, &req)
		f.mu.Lock()
		f.attempts = append(f.attempts, sendAttempt{key: req.Key, text: req.Text, at: time.Now()})
		n := len(f.attempts)
		step := f.script[min(n, len(f.script))-1]
		f.mu.Unlock()
		rec := &codeRecorder{ResponseWriter: w}
		step(rec)
		f.mu.Lock()
		f.attempts[n-1].status = rec.code
		f.mu.Unlock()
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

// delivered son los textos que Néctar aceptó, en orden de llegada.
func (f *fakeNectarSend) delivered() []string {
	var out []string
	for _, a := range f.sent() {
		if a.status >= 200 && a.status < 300 {
			out = append(out, a.text)
		}
	}
	return out
}

func turnWith(messages ...statemachine.OutboundMessage) *statemachine.StateResult {
	return &statemachine.StateResult{NextState: "MAIN_MENU", Messages: messages}
}

func text(s string) statemachine.OutboundMessage { return &statemachine.TextMessage{Text: s} }

func buttons(s string) statemachine.OutboundMessage {
	return &statemachine.ButtonMessage{Text: s, Buttons: []statemachine.Button{{Text: "a", Payload: "a"}}}
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
	if wait, ok := pool.sendRetryWait(sess.ID, sess.PhoneNumber, 1, err); !ok || wait != nectar.MaxSendRetryAfter {
		t.Fatalf("wait = %v, ok = %v", wait, ok)
	}
}

// M-4: los reintentos corren fuera del candado y no dependen del plazo del turno: un turno largo, que
// ya casi lo gastó, tiene los dos igual.
func TestRetrySend_BothRetriesRunInLongTurns(t *testing.T) {
	pool, f, sess := newNectarRetryPool(t, status(http.StatusBadGateway))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	pool.sendAndSave(ctx, sess, sess.PhoneNumber, textResult())

	waitAttempts(t, f, 3, 200*time.Millisecond)
}

// Lo mismo con el 429: su Retry-After se respeta aunque pase del plazo del turno.
func TestRetrySend_RetryAfterRunsPastTurnDeadline(t *testing.T) {
	pool, f, sess := newNectarRetryPool(t, rateLimited("1"), status(http.StatusCreated))
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	pool.sendAndSave(ctx, sess, sess.PhoneNumber, textResult())

	got := waitAttempts(t, f, 2, 50*time.Millisecond)
	if d := got[1].at.Sub(got[0].at); d < 900*time.Millisecond {
		t.Fatalf("el reintento salió a %v, antes del Retry-After", d)
	}
}

// I-1, Néctar cae y vuelve en un turno A+B: llegan los dos, aunque A salga antes en su reintento.
func TestRetrySend_SameTurnBothArriveWhenNectarReturns(t *testing.T) {
	bad := status(http.StatusBadGateway)
	pool, f, sess := newNectarRetryPool(t, bad, bad, bad, bad, status(http.StatusCreated), status(http.StatusCreated))
	pool.retryDelay = 400 * time.Millisecond // más que los 300 ms entre mensajes del turno

	pool.sendAndSave(context.Background(), sess, sess.PhoneNumber, turnWith(buttons("A"), text("B")))

	waitAttempts(t, f, 6, 100*time.Millisecond)
	if got := f.delivered(); !slices.Contains(got, "A") || !slices.Contains(got, "B") {
		t.Fatalf("llegan A y B: %v", got)
	}
}

// Dentro del turno, un hermano entregado no suprime al otro: llegan los dos.
func TestRetrySend_SiblingDeliveredDoesNotSuppress(t *testing.T) {
	bad := status(http.StatusBadGateway)
	pool, f, sess := newNectarRetryPool(t, bad, status(http.StatusCreated), bad, status(http.StatusCreated))
	pool.retryDelay = 400 * time.Millisecond

	pool.sendAndSave(context.Background(), sess, sess.PhoneNumber, turnWith(buttons("A"), text("B")))

	waitAttempts(t, f, 4, 100*time.Millisecond)
	if got := f.delivered(); !slices.Equal(got, []string{"B", "A"}) {
		t.Fatalf("entregados = %v", got)
	}
}

// I-1, dos turnos con Néctar cayendo y volviendo: el éxito del reintento de la vieja no suprime a la
// nueva, que sale siempre, y la vieja nunca llega detrás de ella.
func TestRetrySend_OldRetrySuccessDoesNotSuppressNewer(t *testing.T) {
	bad := status(http.StatusBadGateway)
	pool, f, sess := newNectarRetryPool(t, bad, bad, bad, bad, status(http.StatusCreated))
	pool.retryDelay = 400 * time.Millisecond

	pool.sendAndSave(context.Background(), sess, sess.PhoneNumber, turnWith(text("VIEJA")))
	time.Sleep(150 * time.Millisecond)
	pool.sendAndSave(context.Background(), sess, sess.PhoneNumber, turnWith(text("NUEVA")))

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !slices.Contains(f.delivered(), "NUEVA") {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(3*pool.retryDelay + 100*time.Millisecond)
	got := f.delivered()
	if len(got) == 0 || got[len(got)-1] != "NUEVA" {
		t.Fatalf("la nueva sale y va la última: %v", got)
	}
}

// Si la nueva ya salió a la primera, la vieja se descarta con log, también en su primer reintento.
func TestRetrySend_OldDiscardedOnceNewerDelivered(t *testing.T) {
	logged := captureSendLogs(t)
	pool, f, sess := newNectarRetryPool(t, status(http.StatusBadGateway), status(http.StatusCreated))
	pool.retryDelay = 100 * time.Millisecond

	pool.sendAndSave(context.Background(), sess, sess.PhoneNumber, turnWith(text("VIEJA")))
	pool.sendAndSave(context.Background(), sess, sess.PhoneNumber, turnWith(text("NUEVA")))

	waitAttempts(t, f, 2, 5*pool.retryDelay)
	if got := f.delivered(); !slices.Equal(got, []string{"NUEVA"}) {
		t.Fatalf("entregados = %v", got)
	}
	if !strings.Contains(logged.String(), "turno posterior") {
		t.Fatalf("falta el log del descarte: %s", logged.String())
	}
}

// Y si la nueva salió en SU reintento, tampoco la vieja llega detrás (quitar el sello de los reintentos
// no bastaría: por eso la guarda compara turnos).
func TestRetrySend_OldDiscardedOnceNewerDeliveredOnRetry(t *testing.T) {
	bad := status(http.StatusBadGateway)
	pool, f, sess := newNectarRetryPool(t, bad, bad, bad, status(http.StatusCreated))
	pool.retryDelay = 400 * time.Millisecond

	pool.sendAndSave(context.Background(), sess, sess.PhoneNumber, turnWith(text("VIEJA")))
	time.Sleep(150 * time.Millisecond)
	pool.sendAndSave(context.Background(), sess, sess.PhoneNumber, turnWith(text("NUEVA")))

	waitAttempts(t, f, 4, 3*pool.retryDelay+100*time.Millisecond)
	if got := f.delivered(); !slices.Equal(got, []string{"NUEVA"}) {
		t.Fatalf("entregados = %v", got)
	}
}

// El registro guarda el mayor turno entregado por paciente, nunca retrocede y la limpieza lo purga.
func TestDeliveredTurns(t *testing.T) {
	pool := retryTestPool(&mockMessageSender{}, &mockSessionMgmt{})
	if pool.laterTurnDelivered("nectar:1", 1) {
		t.Fatal("sin envíos")
	}
	pool.markTurnDelivered("nectar:1", 5)
	pool.markTurnDelivered("nectar:1", 3) // el reintento de un turno viejo no lo hace retroceder
	if !pool.laterTurnDelivered("nectar:1", 4) || pool.laterTurnDelivered("nectar:1", 5) ||
		pool.laterTurnDelivered("nectar:2", 4) {
		t.Fatal("solo cuenta un turno mayor, y de ese paciente")
	}
	pool.markTurnDelivered("+573001234567", 9)
	if pool.laterTurnDelivered("+573001234567", 1) {
		t.Fatal("Bird no lleva guarda: sigue con su reintento de siempre")
	}
	pool.purgeStaleLockMisses(time.Now().Add(deliveredTurnTTL + time.Second))
	if pool.laterTurnDelivered("nectar:1", 4) {
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

// lockedBuffer es un búfer que se puede leer mientras un reintento en otra goroutine escribe en el log.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func captureSendLogs(t *testing.T) *lockedBuffer {
	t.Helper()
	logged := &lockedBuffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logged, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return logged
}

// Un 429 que sigue tras los dos reintentos se pierde: deja su evento, no solo el log (el Retry-After de
// 60 s repetido). Con otro fallo no: ese queda en el log de siempre.
func TestRetrySend_RateLimitedAfterAllRetriesLeavesEvent(t *testing.T) {
	sink := &observability.CaptureSink{}
	stop := observability.StartCapture(sink)
	pool, f, sess := newNectarRetryPool(t, rateLimited("1"))

	pool.sendAndSave(context.Background(), sess, sess.PhoneNumber, textResult())
	waitAttempts(t, f, 3, 100*time.Millisecond)
	stop()

	e := sink.FindStep("infra", "send_rate_limited_undelivered")
	if e == nil {
		t.Fatalf("falta el evento: %+v", sink.Events())
	}
	if e.TraceID != observability.TraceSession(sess.ID) || e.Outcome != "error" {
		t.Errorf("evento = %+v", e)
	}
}

func TestRetrySend_Exhausted5xxLeavesNoRateLimitEvent(t *testing.T) {
	sink := &observability.CaptureSink{}
	stop := observability.StartCapture(sink)
	pool, f, sess := newNectarRetryPool(t, status(http.StatusBadGateway))

	pool.sendAndSave(context.Background(), sess, sess.PhoneNumber, textResult())
	waitAttempts(t, f, 3, 100*time.Millisecond)
	stop()

	if e := sink.FindStep("infra", "send_rate_limited_undelivered"); e != nil {
		t.Fatalf("un 502 no es un límite: %+v", e)
	}
}

// M-7: un fallo que no se reintenta (un 4xx no permanente) ya dejó su ERROR al fallar el envío: no
// registra «retry failed» sin haber reintentado, que sería un segundo ERROR hacia Telegram.
func TestRetrySend_NotRetryableDoesNotLogRetryFailed(t *testing.T) {
	logged := captureSendLogs(t)
	pool, f, sess := newNectarRetryPool(t, status(http.StatusBadRequest))

	pool.sendAndSave(context.Background(), sess, sess.PhoneNumber, textResult())
	waitAttempts(t, f, 1, 100*time.Millisecond)

	out := logged.String()
	if strings.Contains(out, "send message retry failed") {
		t.Fatalf("no hubo reintento: %s", out)
	}
	if strings.Count(out, "level=ERROR") != 1 || !strings.Contains(out, "send retry skipped") {
		t.Fatalf("un solo ERROR y el aviso de que no se reintenta: %s", out)
	}
}

// Agotar reintentos sí es «retry failed».
func TestRetrySend_ExhaustedLogsRetryFailed(t *testing.T) {
	logged := captureSendLogs(t)
	pool, f, sess := newNectarRetryPool(t, status(http.StatusBadGateway))

	pool.sendAndSave(context.Background(), sess, sess.PhoneNumber, textResult())
	waitAttempts(t, f, 3, 100*time.Millisecond)

	if out := logged.String(); !strings.Contains(out, "send message retry failed") {
		t.Fatalf("falta el log: %s", out)
	}
}
