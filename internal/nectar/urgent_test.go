package nectar

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/monitor"
)

// Aviso urgente de volver a Bird desde el cliente: cada fallo de transporte o 5xx de Néctar cuenta
// para TriggerNectarDown; cada error del escalado, para TriggerEscalationFailing. Las respuestas de
// negocio (404 «ya no es mía», 409) no son fallos.

type urgentCount struct {
	trigger   monitor.UrgentTrigger
	threshold int
	window    time.Duration
	detail    string
}

type urgentCounter struct {
	mu    sync.Mutex
	calls []urgentCount
}

func (u *urgentCounter) Count(_ context.Context, trigger monitor.UrgentTrigger, threshold int, window time.Duration, detail string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.calls = append(u.calls, urgentCount{trigger, threshold, window, detail})
}

func (u *urgentCounter) of(trigger monitor.UrgentTrigger) []urgentCount {
	u.mu.Lock()
	defer u.mu.Unlock()
	var out []urgentCount
	for _, c := range u.calls {
		if c.trigger == trigger {
			out = append(out, c)
		}
	}
	return out
}

var testUrgentLimits = UrgentLimits{
	DownCount: 5, DownWindow: 5 * time.Minute,
	EscalationFailCount: 3, EscalationFailWindow: 10 * time.Minute,
}

// statusClient arma un cliente contra un Néctar que contesta siempre status (con cuerpo body).
func statusClient(t *testing.T, status int, body string) (*Client, *urgentCounter) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL, svcKey, reqSecret)
	u := &urgentCounter{}
	c.SetUrgentReporter(u, testUrgentLimits)
	return c, u
}

func TestUrgent_5xxCountsAsNectarDown(t *testing.T) {
	c, u := statusClient(t, http.StatusBadGateway, "")
	_ = c.Close(context.Background(), 1234)
	got := u.of(monitor.TriggerNectarDown)
	if len(got) != 1 || got[0].threshold != 5 || got[0].window != 5*time.Minute {
		t.Fatalf("un 502 cuenta con el umbral y la ventana configurados: %+v", got)
	}
	if got[0].detail != "último: HTTP 502" {
		t.Fatalf("el detalle es la clase: %q", got[0].detail)
	}
}

func TestUrgent_TransportFailureCountsAsNectarDown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.Close() // conexión rechazada
	c := NewClient(srv.URL, svcKey, reqSecret)
	u := &urgentCounter{}
	c.SetUrgentReporter(u, testUrgentLimits)
	_, _ = c.GetState(context.Background(), 1234)
	if got := u.of(monitor.TriggerNectarDown); len(got) != 1 {
		t.Fatalf("un fallo de transporte cuenta: %+v", got)
	}
}

// La apertura (doOpen) y el horario pasan por el mismo armado: un 5xx de la apertura también cuenta.
func TestUrgent_OpenFailureCountsAsNectarDown(t *testing.T) {
	c, u := statusClient(t, http.StatusInternalServerError, "")
	b := NewBridge(c, map[string]string{"p": "plantilla_x"})
	b.ChannelID = 9
	_, _, _ = b.OpenWithTemplate(context.Background(), "+573001112233", bird.TemplateConfig{ProjectID: "p"})
	got := u.of(monitor.TriggerNectarDown)
	if len(got) == 0 {
		t.Fatal("el 500 de la apertura cuenta")
	}
	if strings.Contains(got[0].detail, "3001112233") {
		t.Fatalf("sin teléfonos en el detalle: %q", got[0].detail)
	}
}

func TestUrgent_BusinessAnswersAreNotFailures(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
	}{
		{http.StatusNotFound, ""},
		{http.StatusConflict, `{"reason":"window_closed"}`},
		{http.StatusOK, `{}`},
	} {
		c, u := statusClient(t, tc.status, tc.body)
		_, _ = c.SendText(context.Background(), 1234, "hola")
		if got := u.of(monitor.TriggerNectarDown); len(got) != 0 {
			t.Fatalf("status %d no es un fallo de Néctar: %+v", tc.status, got)
		}
	}
}

func TestUrgent_CanceledContextIsNotNectarDown(t *testing.T) {
	c, u := statusClient(t, http.StatusOK, `{}`)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = c.SendText(ctx, 1234, "hola")
	if got := u.of(monitor.TriggerNectarDown); len(got) != 0 {
		t.Fatalf("un apagado del bot no es Néctar caído: %+v", got)
	}
}

// Los escalados que fallan sin que Néctar esté caído (credenciales, escalado apagado, un status sin
// traducción) cuentan para EscalationFailing, con código y motivo pero sin el cuerpo.
func TestUrgent_EscalationErrorCounts(t *testing.T) {
	for _, tc := range []struct {
		status    int
		body      string
		wantClass string
	}{
		{http.StatusUnauthorized, "", "HTTP 401"},
		{http.StatusForbidden, `{"reason":"escalation_disabled"}`, "HTTP 403 escalation_disabled"},
		{http.StatusForbidden, `{"reason":"otra","detail":"+573001112233"}`, "HTTP 403 otra"},
		{http.StatusBadRequest, `{"detail":"cuerpo largo +573001112233"}`, "HTTP 400"},
	} {
		c, u := statusClient(t, tc.status, tc.body)
		b := NewBridge(c, nil)
		if err := b.Escalate(context.Background(), 1234, bird.Escalation{Reason: "bot_cannot_answer", Summary: "s"}); err == nil {
			t.Fatalf("status %d es error", tc.status)
		}
		got := u.of(monitor.TriggerEscalationFailing)
		if len(got) != 1 || got[0].threshold != 3 || got[0].window != 10*time.Minute {
			t.Fatalf("status %d: el escalado fallido cuenta con su umbral y ventana: %+v", tc.status, got)
		}
		if got[0].detail != "último: "+tc.wantClass {
			t.Fatalf("status %d: detalle %q, se esperaba la clase %q sin cuerpo", tc.status, got[0].detail, tc.wantClass)
		}
	}
}

// Un 5xx o un fallo de transporte del escalado es Néctar caído: cuenta UNA vez, en NectarDown, y no
// también como escalado fallido (un mismo corte no da dos avisos).
func TestUrgent_EscalationOutageCountsOnlyAsDown(t *testing.T) {
	c, u := statusClient(t, http.StatusServiceUnavailable, `{"detail":"x"}`)
	_ = NewBridge(c, nil).Escalate(context.Background(), 1234, bird.Escalation{Reason: "bot_cannot_answer", Summary: "s"})
	if got := u.of(monitor.TriggerEscalationFailing); len(got) != 0 {
		t.Fatalf("el 503 no cuenta como escalado fallido: %+v", got)
	}
	if got := u.of(monitor.TriggerNectarDown); len(got) != 1 {
		t.Fatalf("el 503 cuenta como Néctar caído: %+v", got)
	}
}

// El detalle del aviso es solo la clase: ni el cuerpo de Néctar ni la URL.
func TestUrgent_DownDetailIsOnlyTheClass(t *testing.T) {
	c, u := statusClient(t, http.StatusInternalServerError, `{"detail":"+573001112233 boom"}`)
	_ = c.Close(context.Background(), 1234)
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.Close()
	down := NewClient(srv.URL, svcKey, reqSecret)
	down.SetUrgentReporter(u, testUrgentLimits)
	_ = down.Close(context.Background(), 1234)

	got := u.of(monitor.TriggerNectarDown)
	if len(got) != 2 || got[0].detail != "último: HTTP 500" || got[1].detail != "último: conexión rechazada" {
		t.Fatalf("detalles = %+v", got)
	}
}

// Un Telegram colgado no frena a Néctar: el 502 meta_rejected (que se distingue por el cuerpo) sigue
// siendo plantilla rechazada y la apertura vuelve enseguida, con el aviso real (sin Start, la cola
// tampoco bloquea).
func TestUrgent_BlockedTelegramDoesNotSlowOrMisclassify(t *testing.T) {
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	alerter := monitor.NewUrgentAlerter(func(context.Context, string) error { <-block; return nil },
		"docs/GUIA-VOLVER-A-BIRD.md", time.Minute, time.Now)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go alerter.Start(ctx)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"reason":"meta_rejected"}`))
	}))
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL, svcKey, reqSecret)
	c.SetUrgentReporter(alerter, UrgentLimits{DownCount: 1, DownWindow: time.Minute, EscalationFailCount: 1, EscalationFailWindow: time.Minute})
	b := NewBridge(c, map[string]string{"p": "plantilla_x"})
	b.ChannelID = 9

	for i := 0; i < 3; i++ {
		start := time.Now()
		_, _, err := b.OpenWithTemplate(context.Background(), "+573001112233", bird.TemplateConfig{ProjectID: "p"})
		if !errors.Is(err, ErrTemplateRejected) {
			t.Fatalf("intento %d: el 502 meta_rejected es plantilla rechazada, no %v", i, err)
		}
		if d := time.Since(start); d > time.Second {
			t.Fatalf("intento %d: la apertura tardó %s con Telegram colgado", i, d)
		}
	}
}

// El 404 del escalado es «ya no es mía» (otro la tomó): respuesta de negocio, no un escalado roto.
func TestUrgent_EscalationNotMineDoesNotCount(t *testing.T) {
	c, u := statusClient(t, http.StatusNotFound, "")
	_ = NewBridge(c, nil).Escalate(context.Background(), 1234, bird.Escalation{Reason: "bot_cannot_answer", Summary: "s"})
	if got := u.of(monitor.TriggerEscalationFailing); len(got) != 0 {
		t.Fatalf("el 404 no cuenta: %+v", got)
	}
}

func TestUrgent_NoReporterIsNoop(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL, svcKey, reqSecret)
	if err := c.Close(context.Background(), 1); err == nil {
		t.Fatal("el 502 sigue siendo error")
	}
}

// M-3: un envío abandonado por el plazo del turno no prueba que Néctar esté caída: no llegó a tener los
// 15 s del cliente. Solo ese, marcado por quien envía.
func TestUrgent_TurnDeadlineIsNotNectarDown(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	defer srv.Close()
	defer close(release)
	c := NewClient(srv.URL, svcKey, reqSecret)
	u := &urgentCounter{}
	c.SetUrgentReporter(u, testUrgentLimits)

	ctx, cancel := context.WithTimeout(MarkTurnSend(context.Background()), 50*time.Millisecond)
	defer cancel()
	if _, err := c.SendText(ctx, 1234, "hola"); err == nil {
		t.Fatal("quería el error del plazo")
	}
	if got := u.of(monitor.TriggerNectarDown); len(got) != 0 {
		t.Fatalf("el plazo del turno no es Néctar caído: %+v", got)
	}
}

// M-10: cualquier otro plazo corto sí cuenta, como la lectura de contacto de 5 s con Néctar colgada.
func TestUrgent_OtherCallerDeadlineCountsAsNectarDown(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	defer srv.Close()
	defer close(release)
	c := NewClient(srv.URL, svcKey, reqSecret)
	u := &urgentCounter{}
	c.SetUrgentReporter(u, testUrgentLimits)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := c.GetState(ctx, 1234); err == nil {
		t.Fatal("quería el error del plazo")
	}
	if got := u.of(monitor.TriggerNectarDown); len(got) != 1 {
		t.Fatalf("la lectura colgada cuenta: %+v", got)
	}
}

// Un envío del turno que falla sin que venza su plazo cuenta como siempre.
func TestUrgent_TurnSendCountsIfDeadlineNotReached(t *testing.T) {
	c, u := statusClient(t, http.StatusBadGateway, "")
	_, _ = c.SendText(MarkTurnSend(context.Background()), 1234, "hola")
	if got := u.of(monitor.TriggerNectarDown); len(got) != 1 {
		t.Fatalf("el 502 cuenta: %+v", got)
	}
}

// El timeout del propio cliente sí cuenta: Néctar tuvo su tiempo y no contestó.
func TestUrgent_ClientTimeoutCountsAsNectarDown(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	defer srv.Close()
	defer close(release)
	c := NewClient(srv.URL, svcKey, reqSecret)
	c.http.Timeout = 50 * time.Millisecond
	u := &urgentCounter{}
	c.SetUrgentReporter(u, testUrgentLimits)

	_, _ = c.SendText(context.Background(), 1234, "hola")

	got := u.of(monitor.TriggerNectarDown)
	if len(got) != 1 || got[0].detail != "último: timeout" {
		t.Fatalf("el timeout del cliente cuenta: %+v", got)
	}
}

// M-2: un mensaje que agota sus reintentos cuenta lo mismo que antes de haber dos: el envío y el primer
// reintento. Con el umbral de 5, el aviso sigue saltando con el tercer mensaje fallido, no con el segundo.
func TestUrgent_ExtraRetryDoesNotCountAsNectarDown(t *testing.T) {
	c, u := statusClient(t, http.StatusBadGateway, "")
	failMessage := func(text string) {
		for range MaxSendRetries + 1 { // el envío y sus reintentos: misma clave en duda
			_, _ = c.SendText(context.Background(), 1234, text)
		}
	}

	failMessage("primero")
	if got := u.of(monitor.TriggerNectarDown); len(got) != 2 {
		t.Fatalf("un mensaje cuenta 2 (envío y primer reintento): %d", len(got))
	}
	failMessage("segundo")
	if got := u.of(monitor.TriggerNectarDown); len(got) >= testUrgentLimits.DownCount {
		t.Fatalf("con 2 mensajes no llega al umbral: %d", len(got))
	}
	failMessage("tercero")
	if got := u.of(monitor.TriggerNectarDown); len(got) < testUrgentLimits.DownCount {
		t.Fatalf("con 3 mensajes llega al umbral: %d", len(got))
	}
}

// Lo mismo con el transporte, y sin tocar lo demás: una lectura o un mensaje nuevo cuentan siempre.
func TestUrgent_ExtraRetryTransportDoesNotCount(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.Close()
	c := NewClient(srv.URL, svcKey, reqSecret)
	u := &urgentCounter{}
	c.SetUrgentReporter(u, testUrgentLimits)
	for range MaxSendRetries + 1 {
		_, _ = c.SendText(context.Background(), 1234, "hola")
	}
	if got := u.of(monitor.TriggerNectarDown); len(got) != 2 {
		t.Fatalf("transporte: %d", len(got))
	}
	_, _ = c.GetState(context.Background(), 1234)
	if got := u.of(monitor.TriggerNectarDown); len(got) != 3 {
		t.Fatalf("la lectura cuenta: %d", len(got))
	}
}
