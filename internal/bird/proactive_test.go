package bird

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/monitor"
	"github.com/neuro-bot/neuro-bot/internal/testutil/fakeurgent"
)

// SendProactiveTemplate es la puerta de las plantillas que INICIAN la conversación (recordatorios,
// lista de espera, avisos administrativos). Sin puente es SendTemplate tal cual; con puente abre la
// conversación en Néctar y devuelve la clave de sesión con la que contestará el paciente.

var proactiveTmpl = TemplateConfig{
	ProjectID: "proj-recordatorio",
	VersionID: "v1",
	Locale:    "es",
	Params:    []TemplateParam{{Type: "string", Key: "patient_name", Value: "Ana"}},
}

// recordingBird es un Bird que contesta 200 y guarda cada cuerpo, para comparar byte a byte.
func recordingBird(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, r.Method+" "+r.URL.Path+" "+string(b))
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"msg-bird"}`))
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), bodies...)
	}
}

func TestSendProactiveTemplate_BirdUnchanged(t *testing.T) {
	srv, got := recordingBird(t)
	c := NewClientForTest(srv.URL)

	viaTemplate, err := c.SendTemplate("+573001112233", proactiveTmpl)
	if err != nil {
		t.Fatal(err)
	}
	sent, err := c.SendProactiveTemplate(context.Background(), "+573001112233", proactiveTmpl)
	if err != nil {
		t.Fatal(err)
	}
	if sent.MessageID != viaTemplate || sent.Key != "+573001112233" {
		t.Fatalf("sin puente la clave es el teléfono y el id el de Bird: %+v", sent)
	}
	if sent.NectarConversationID() != "" || sent.ContactPhone("+573001112233") != "" {
		t.Fatalf("por Bird no hay conversación de Néctar ni contacto aparte: %+v", sent)
	}
	reqs := got()
	if len(reqs) != 2 || reqs[0] != reqs[1] {
		t.Fatalf("la petición a Bird es byte a byte la de SendTemplate:\n%v", reqs)
	}
}

func TestSendProactiveTemplate_NectarOpens(t *testing.T) {
	c, fb, stub := newBridgedClient(t)
	fb.openID, fb.openMsg = 77, "901"

	sent, err := c.SendProactiveTemplate(context.Background(), "+573001112233", proactiveTmpl)
	if err != nil {
		t.Fatal(err)
	}
	if len(fb.openPhones) != 1 || fb.openPhones[0] != "+573001112233" {
		t.Fatalf("se abre al teléfono del paciente: %v", fb.openPhones)
	}
	if sent.Key != "nectar:77" || sent.MessageID != "901" {
		t.Fatalf("la clave es la de la conversación abierta: %+v", sent)
	}
	if sent.NectarConversationID() != "77" || sent.ContactPhone("+573001112233") != "+573001112233" {
		t.Fatalf("conversación %q contacto %q", sent.NectarConversationID(), sent.ContactPhone("+573001112233"))
	}
	if fb.templates != 0 {
		t.Fatalf("la apertura YA lleva la plantilla: no se manda otra (%d)", fb.templates)
	}
	if reqs := stub.requests(); len(reqs) != 0 {
		t.Fatalf("Bird no recibe nada: %v", reqs)
	}
}

func TestSendProactiveTemplate_AlreadyOpenMine(t *testing.T) {
	c, fb, stub := newBridgedClient(t)
	fb.openErr = &AlreadyOpenError{ConversationID: 55, Mine: true}

	sent, err := c.SendProactiveTemplate(context.Background(), "+573001112233", proactiveTmpl)
	if err != nil {
		t.Fatal(err)
	}
	if len(fb.templateIDs) != 1 || fb.templateIDs[0] != 55 {
		t.Fatalf("la plantilla va a la conversación viva del bot: %v", fb.templateIDs)
	}
	if sent.Key != "nectar:55" || sent.MessageID != "n-4" {
		t.Fatalf("la clave es la de la conversación viva: %+v", sent)
	}
	if reqs := stub.requests(); len(reqs) != 0 {
		t.Fatalf("Bird no recibe nada: %v", reqs)
	}
}

func TestSendProactiveTemplate_AlreadyOpenMineWithoutID(t *testing.T) {
	// 409 already_open con un cuerpo ilegible: no hay conversación a la que escribir. Permanente.
	c, fb, _ := newBridgedClient(t)
	fb.openErr = &AlreadyOpenError{ConversationID: 0, Mine: true}
	_, err := c.SendProactiveTemplate(context.Background(), "+573001112233", proactiveTmpl)
	if !errors.Is(err, ErrPermanentSend) || fb.templates != 0 {
		t.Fatalf("err=%v templates=%d", err, fb.templates)
	}
}

func TestSendProactiveTemplate_AlreadyOpenAgent(t *testing.T) {
	c, fb, _ := newBridgedClient(t)
	fb.openErr = &AlreadyOpenError{ConversationID: 55, Mine: false}

	sent, err := c.SendProactiveTemplate(context.Background(), "+573001112233", proactiveTmpl)
	if !errors.Is(err, ErrConversationWithAgent) || !errors.Is(err, ErrPermanentSend) {
		t.Fatalf("la conversación es de un agente: %v", err)
	}
	if sent != (ProactiveSend{}) || fb.templates != 0 {
		t.Fatalf("no se escribe en la conversación de un agente: %+v templates=%d", sent, fb.templates)
	}
}

func TestSendProactiveTemplate_OpenErrorPropagates(t *testing.T) {
	c, fb, _ := newBridgedClient(t)
	boom := errors.New("en duda")
	fb.openErr = boom
	if _, err := c.SendProactiveTemplate(context.Background(), "+573001112233", proactiveTmpl); !errors.Is(err, boom) {
		t.Fatalf("el error de la apertura llega tal cual: %v", err)
	}
	if fb.templates != 0 || len(fb.openPhones) != 1 {
		t.Fatalf("una sola apertura, sin reintento ni plantilla aparte: %+v", fb)
	}
}

func TestSendProactiveTemplate_KillSwitchAndRateLimit(t *testing.T) {
	c := &Client{notifWADisabled: true}
	fb := &fakeBridge{openID: 77}
	c.SetNectar(fb)
	if _, err := c.SendProactiveTemplate(context.Background(), "+573001112233", proactiveTmpl); !errors.Is(err, ErrWhatsAppNotificationsDisabled) {
		t.Fatalf("el kill switch va antes de abrir: %v", err)
	}
	if !fb.untouched() {
		t.Fatalf("con el interruptor apagado el puente no ve nada: %+v", fb)
	}

	c2, fb2, _ := newBridgedClient(t)
	fb2.openID = 77
	c2.sendRateLimit = 1
	if _, err := c2.SendProactiveTemplate(context.Background(), "+573001112233", proactiveTmpl); err != nil {
		t.Fatalf("el primero pasa: %v", err)
	}
	if _, err := c2.SendProactiveTemplate(context.Background(), "+573001112233", proactiveTmpl); !errors.Is(err, ErrSendRateLimited) {
		t.Fatalf("el segundo se corta por tope antes de abrir: %v", err)
	}
	if len(fb2.openPhones) != 1 {
		t.Fatalf("solo la primera apertura llega al puente: %v", fb2.openPhones)
	}
}

func TestSendProactiveTemplate_SessionKeyUsesItsConversation(t *testing.T) {
	// Quien ya tiene la clave de sesión (una conversación viva de Néctar) no abre otra: la
	// plantilla va a esa conversación, como hacía SendTemplate.
	c, fb, _ := newBridgedClient(t)
	sent, err := c.SendProactiveTemplate(context.Background(), "nectar:42", proactiveTmpl)
	if err != nil || sent.Key != "nectar:42" || sent.ContactPhone("nectar:42") != "" {
		t.Fatalf("sent=%+v err=%v", sent, err)
	}
	if len(fb.openPhones) != 0 || len(fb.templateIDs) != 1 || fb.templateIDs[0] != 42 {
		t.Fatalf("sin apertura, plantilla a la 42: %+v", fb)
	}
}

// Un identificador que no es E.164 (username/BSUID) no puede abrir por teléfono: ErrNonContactable
// antes de cualquier llamada, ni a Néctar ni a Bird.
func TestSendProactiveTemplate_NonE164NeverOpens(t *testing.T) {
	c, fb, stub := newBridgedClient(t)
	fb.openID = 77
	_, err := c.SendProactiveTemplate(context.Background(), "laura.perez", proactiveTmpl)
	if !errors.Is(err, ErrNonContactable) {
		t.Fatalf("quiero ErrNonContactable, tengo %v", err)
	}
	if !fb.untouched() || len(stub.requests()) != 0 {
		t.Fatalf("ninguna llamada: puente=%+v bird=%v", fb, stub.requests())
	}
}

// ReportProactiveConfigStop es la ÚNICA secuencia de una corrida detenida por configuración que
// comparten los tres lotes (avisos administrativos, recordatorios, lista de espera): un Error con la
// corrida y cuántos quedaron, y el aviso urgente de volver a Bird. Sin aviso cableado, solo el Error.
func TestReportProactiveConfigStop_LogsOnceAndFires(t *testing.T) {
	var logged bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, nil)))
	defer slog.SetDefault(previous)

	cause := fmt.Errorf("%w: 409 template_other_account", ErrProactiveConfig)
	urgent := &fakeurgent.Recorder{}
	ReportProactiveConfigStop(context.Background(), urgent, "waiting_list", cause, 3)
	fires := urgent.Fires()
	if len(fires) != 1 || fires[0].Trigger != monitor.TriggerProactiveStopped ||
		!strings.Contains(fires[0].Detail, "waiting_list") || !strings.Contains(fires[0].Detail, "unsent=3") {
		t.Fatalf("un aviso con la corrida y cuántos quedaron: %+v", fires)
	}
	if strings.Count(logged.String(), "level=ERROR") != 1 {
		t.Fatalf("un único Error por corrida: %s", logged.String())
	}

	logged.Reset()
	ReportProactiveConfigStop(context.Background(), nil, "confirmation", cause, 1)
	if strings.Count(logged.String(), "level=ERROR") != 1 {
		t.Fatalf("sin aviso cableado sigue el Error: %s", logged.String())
	}
}

// legacyBridge es un puente que reparte por línea: legacy dice si IsLegacy contesta sí, y contact es
// lo que publica Contact.
type legacyBridge struct {
	*fakeBridge
	legacy  bool
	contact NectarContact
	asks    int
}

func (l *legacyBridge) IsLegacy(context.Context, int64) bool { l.asks++; return l.legacy }

func (l *legacyBridge) Contact(context.Context, int64) (NectarContact, error) {
	return l.contact, nil
}

func newLegacyClient(t *testing.T, legacy bool, identifier string) (*Client, *legacyBridge) {
	t.Helper()
	srv, _ := newBirdStub(t)
	c := NewClientForTest(srv.URL)
	lb := &legacyBridge{
		fakeBridge: &fakeBridge{openID: 90, openMsg: "902"}, legacy: legacy,
		contact: NectarContact{Identifier: identifier},
	}
	c.SetNectar(lb)
	return c, lb
}

func TestSendTemplate_LegacyConversationReopensOnMain(t *testing.T) {
	c, lb := newLegacyClient(t, true, "573001112233")
	id, err := c.SendTemplate("nectar:42", proactiveTmpl)
	if err != nil || id != "902" {
		t.Fatalf("id=%q err=%v", id, err)
	}
	if len(lb.openPhones) != 1 || lb.openPhones[0] != "+573001112233" {
		t.Fatalf("se abre por la nueva al teléfono con +: %v", lb.openPhones)
	}
	if lb.templates != 0 {
		t.Fatalf("no se manda la plantilla a la conversación vieja: %v", lb.templateIDs)
	}
}

func TestSendTemplate_LegacyWithoutContactFailsNoSend(t *testing.T) {
	c, lb := newLegacyClient(t, true, "")
	if _, err := c.SendTemplate("nectar:42", proactiveTmpl); !errors.Is(err, ErrNonContactable) {
		t.Fatalf("sin teléfono no hay envío: %v", err)
	}
	if !lb.untouched() {
		t.Fatalf("ninguna llamada de envío: %+v", lb.fakeBridge)
	}
}

func TestSendTemplate_MainConversationUnchanged(t *testing.T) {
	c, lb := newLegacyClient(t, false, "573001112233")
	if _, err := c.SendTemplate("nectar:42", proactiveTmpl); err != nil {
		t.Fatal(err)
	}
	if len(lb.templateIDs) != 1 || lb.templateIDs[0] != 42 || len(lb.openPhones) != 0 {
		t.Fatalf("la principal sigue igual: %+v", lb.fakeBridge)
	}
}

func TestSendProactiveTemplate_LegacySessionKeyReopensOnMain(t *testing.T) {
	c, lb := newLegacyClient(t, true, "573001112233")
	if _, err := c.SendProactiveTemplate(context.Background(), "nectar:42", proactiveTmpl); err != nil {
		t.Fatal(err)
	}
	if len(lb.openPhones) != 1 || lb.templates != 0 {
		t.Fatalf("la rama nectar: también reabre por la principal: %+v", lb.fakeBridge)
	}
}
