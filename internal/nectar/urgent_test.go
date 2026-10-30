package nectar

import (
	"context"
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
	if !strings.Contains(got[0].detail, "502") {
		t.Fatalf("el detalle dice el status: %q", got[0].detail)
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

func TestUrgent_EscalationErrorCounts(t *testing.T) {
	c, u := statusClient(t, http.StatusServiceUnavailable, "")
	b := NewBridge(c, nil)
	if err := b.Escalate(context.Background(), 1234, bird.Escalation{Reason: "bot_cannot_answer", Summary: "s"}); err == nil {
		t.Fatal("el 503 es error")
	}
	got := u.of(monitor.TriggerEscalationFailing)
	if len(got) != 1 || got[0].threshold != 3 || got[0].window != 10*time.Minute {
		t.Fatalf("el escalado fallido cuenta con su umbral y ventana: %+v", got)
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
