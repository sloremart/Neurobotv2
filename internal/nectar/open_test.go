package nectar

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/neuro-bot/neuro-bot/internal/bird"
)

// La apertura con plantilla (Plan 44 §1.1 de Néctar) contra un falso Néctar con GUION: cada
// llamada a POST /api/bot/conversations/open consume la siguiente respuesta de la cola. Exige
// lo mismo que el real (firma X-Nectar-* y clave de servicio) para que la prueba demuestre que
// la apertura reutiliza el armado de la petición y no uno propio.

type scriptedReply struct {
	status     int
	retryAfter string // "" = sin cabecera
	body       string
}

type fakeOpen struct {
	srv    *httptest.Server
	mu     sync.Mutex
	script []scriptedReply
	bodies [][]byte
}

func newFakeOpen(t *testing.T, script ...scriptedReply) *fakeOpen {
	f := &fakeOpen{script: script}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		full := "http://" + r.Host + r.URL.String()
		if err := VerifyNotice(reqSecret, r.Header.Get(SignatureHeader), r.Header.Get(TimestampHeader), full, body); err != nil {
			t.Errorf("firma rechazada: %v", err)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Header.Get(ServiceKeyHeader) != svcKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/api/bot/conversations/open" {
			t.Errorf("ruta inesperada: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		f.mu.Lock()
		f.bodies = append(f.bodies, body)
		if len(f.script) == 0 {
			f.mu.Unlock()
			t.Error("llamada de más: el guion se agotó")
			w.WriteHeader(http.StatusTeapot)
			return
		}
		reply := f.script[0]
		f.script = f.script[1:]
		f.mu.Unlock()
		if reply.retryAfter != "" {
			w.Header().Set("Retry-After", reply.retryAfter)
		}
		if reply.body != "" {
			w.Header().Set("Content-Type", "application/json")
		}
		w.WriteHeader(reply.status)
		_, _ = w.Write([]byte(reply.body))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeOpen) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.bodies)
}

// newOpenClient arma el cliente con el reloj de espera falso: anota cuánto se le pidió esperar
// y no duerme.
func newOpenClient(f *fakeOpen) (*Client, *[]time.Duration) {
	c := NewClient(f.srv.URL, svcKey, reqSecret)
	waits := &[]time.Duration{}
	c.sleep = func(_ context.Context, d time.Duration) error {
		*waits = append(*waits, d)
		return nil
	}
	return c, waits
}

var openTmpl = TemplateBody{Name: "recordatorio_cita", Language: "es", Parameters: map[string]string{"1": "Ana"}}

const opened201 = `{"conversation_id":77,"channel_id":7,"message_id":901}`

func TestOpenConversation_201(t *testing.T) {
	f := newFakeOpen(t, scriptedReply{status: http.StatusCreated, body: opened201})
	c, _ := newOpenClient(f)

	got, err := c.OpenConversation(context.Background(), 7, OpenRecipient{Phone: "573001112233"}, openTmpl, "")
	if err != nil {
		t.Fatal(err)
	}
	if got != (Opened{ConversationID: 77, ChannelID: 7, MessageID: 901}) {
		t.Fatalf("abierta = %+v", got)
	}

	var sent struct {
		Key       string                     `json:"idempotency_key"`
		ChannelID int64                      `json:"channel_id"`
		Recipient map[string]string          `json:"recipient"`
		Template  map[string]json.RawMessage `json:"template"`
		Name      *string                    `json:"name"`
	}
	if err := json.Unmarshal(f.bodies[0], &sent); err != nil {
		t.Fatalf("cuerpo: %v %s", err, f.bodies[0])
	}
	if _, err := uuid.Parse(sent.Key); err != nil {
		t.Fatalf("idempotency_key tiene que ser UUID: %q", sent.Key)
	}
	if sent.ChannelID != 7 {
		t.Fatalf("channel_id = %d", sent.ChannelID)
	}
	// Exactamente UNA llave en recipient: Néctar rechaza las dos (recipient_ambiguous).
	if !reflect.DeepEqual(sent.Recipient, map[string]string{"phone": "573001112233"}) {
		t.Fatalf("recipient = %v", sent.Recipient)
	}
	var params map[string]string
	if err := json.Unmarshal(sent.Template["parameters"], &params); err != nil {
		t.Fatalf("parameters va como OBJETO (marcador → valor): %s", f.bodies[0])
	}
	if string(sent.Template["name"]) != `"recordatorio_cita"` || string(sent.Template["language"]) != `"es"` ||
		!reflect.DeepEqual(params, map[string]string{"1": "Ana"}) {
		t.Fatalf("template = %s", f.bodies[0])
	}
	// name es opcional: vacío no viaja.
	if sent.Name != nil {
		t.Fatalf("name vacío no viaja: %s", f.bodies[0])
	}
}

func TestOpenConversation_SendsNameWhenGiven(t *testing.T) {
	f := newFakeOpen(t, scriptedReply{status: http.StatusCreated, body: opened201})
	c, _ := newOpenClient(f)
	if _, err := c.OpenConversation(context.Background(), 7, OpenRecipient{BSUID: "CO.abc"}, openTmpl, "Ana Pérez"); err != nil {
		t.Fatal(err)
	}
	var sent struct {
		Recipient map[string]string `json:"recipient"`
		Name      string            `json:"name"`
	}
	_ = json.Unmarshal(f.bodies[0], &sent)
	if sent.Name != "Ana Pérez" || !reflect.DeepEqual(sent.Recipient, map[string]string{"bsuid": "CO.abc"}) {
		t.Fatalf("cuerpo: %s", f.bodies[0])
	}
}

func TestOpenConversation_AlreadyOpen(t *testing.T) {
	for _, mine := range []bool{false, true} {
		body := `{"error":"ya abierta","reason":"already_open","conversation_id":55,"mine":false}`
		if mine {
			body = `{"error":"ya abierta","reason":"already_open","conversation_id":55,"mine":true}`
		}
		f := newFakeOpen(t, scriptedReply{status: http.StatusConflict, body: body})
		c, _ := newOpenClient(f)
		_, err := c.OpenConversation(context.Background(), 7, OpenRecipient{Phone: "573001112233"}, openTmpl, "")
		var already *AlreadyOpenError
		if !errors.As(err, &already) {
			t.Fatalf("mine=%v: quiero *AlreadyOpenError, tengo %v", mine, err)
		}
		if *already != (AlreadyOpenError{ConversationID: 55, Mine: mine}) {
			t.Fatalf("mine=%v: %+v", mine, *already)
		}
		if f.calls() != 1 {
			t.Fatalf("already_open no se reintenta: %d llamadas", f.calls())
		}
	}
}

func TestOpenConversation_503RetryAfter(t *testing.T) {
	t.Run("dos 503 y luego 201", func(t *testing.T) {
		f := newFakeOpen(t,
			scriptedReply{status: http.StatusServiceUnavailable, retryAfter: "0"},
			scriptedReply{status: http.StatusServiceUnavailable, retryAfter: "0"},
			scriptedReply{status: http.StatusCreated, body: opened201},
		)
		c, waits := newOpenClient(f)
		got, err := c.OpenConversation(context.Background(), 7, OpenRecipient{Phone: "573001112233"}, openTmpl, "")
		if err != nil || got.ConversationID != 77 {
			t.Fatalf("got=%+v err=%v", got, err)
		}
		if f.calls() != 3 || len(*waits) != 2 {
			t.Fatalf("llamadas=%d esperas=%v", f.calls(), *waits)
		}
		// La MISMA clave en los tres intentos: es lo que hace seguro el reintento.
		keys := map[string]bool{}
		for _, b := range f.bodies {
			var k struct {
				Key string `json:"idempotency_key"`
			}
			_ = json.Unmarshal(b, &k)
			keys[k.Key] = true
		}
		if len(keys) != 1 {
			t.Fatalf("los reintentos tienen que reusar la clave: %v", keys)
		}
	})

	t.Run("espera lo que dice la cabecera", func(t *testing.T) {
		f := newFakeOpen(t,
			scriptedReply{status: http.StatusServiceUnavailable, retryAfter: "2"},
			scriptedReply{status: http.StatusCreated, body: opened201},
		)
		c, waits := newOpenClient(f)
		if _, err := c.OpenConversation(context.Background(), 7, OpenRecipient{Phone: "573001112233"}, openTmpl, ""); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(*waits, []time.Duration{2 * time.Second}) {
			t.Fatalf("esperas = %v", *waits)
		}
	})

	t.Run("tres 503 dan error", func(t *testing.T) {
		f := newFakeOpen(t,
			scriptedReply{status: http.StatusServiceUnavailable, retryAfter: "0"},
			scriptedReply{status: http.StatusServiceUnavailable, retryAfter: "0"},
			scriptedReply{status: http.StatusServiceUnavailable, retryAfter: "0"},
		)
		c, _ := newOpenClient(f)
		if _, err := c.OpenConversation(context.Background(), 7, OpenRecipient{Phone: "573001112233"}, openTmpl, ""); err == nil {
			t.Fatal("tres 503 seguidos son error")
		}
		if f.calls() != 3 {
			t.Fatalf("como mucho 2 reintentos: %d llamadas", f.calls())
		}
	})

	t.Run("503 sin Retry-After no se reintenta", func(t *testing.T) {
		// Es el 503 «arrancó sin cliente de Meta»: reintentar en 2 s falla igual.
		f := newFakeOpen(t, scriptedReply{status: http.StatusServiceUnavailable})
		c, waits := newOpenClient(f)
		if _, err := c.OpenConversation(context.Background(), 7, OpenRecipient{Phone: "573001112233"}, openTmpl, ""); err == nil {
			t.Fatal("503 es error")
		}
		if f.calls() != 1 || len(*waits) != 0 {
			t.Fatalf("llamadas=%d esperas=%v", f.calls(), *waits)
		}
	})
}

func TestOpenConversation_NoRetryOnDoubt(t *testing.T) {
	cases := []scriptedReply{
		{status: http.StatusConflict, body: `{"error":"x","reason":"attempt_in_doubt"}`},
		{status: http.StatusConflict, body: `{"error":"x","reason":"attempt_sent_unrecorded"}`},
		{status: http.StatusBadGateway, body: `{"error":"x","reason":"meta_no_answer"}`},
		{status: http.StatusBadGateway, body: `{"error":"x","reason":"sent_but_not_recorded"}`},
		// Un 502 de un proxy (sin cuerpo JSON) o con un motivo desconocido también está en duda.
		{status: http.StatusBadGateway},
		{status: http.StatusBadGateway, body: `<html>502 Bad Gateway</html>`},
		{status: http.StatusBadGateway, body: `{"error":"x","reason":"algo_nuevo"}`},
		// Un 504 de un proxy: la petición pudo llegar a Néctar y la plantilla salir.
		{status: http.StatusGatewayTimeout},
	}
	for _, reply := range cases {
		f := newFakeOpen(t, reply)
		c, _ := newOpenClient(f)
		_, err := c.OpenConversation(context.Background(), 7, OpenRecipient{Phone: "573001112233"}, openTmpl, "")
		if !errors.Is(err, ErrOpenInDoubt) || !bird.IsPermanentSendError(err) {
			t.Fatalf("%d %s: quiero ErrOpenInDoubt permanente, tengo %v", reply.status, reply.body, err)
		}
		if f.calls() != 1 {
			t.Fatalf("%s: en duda NO se reintenta: %d llamadas", reply.body, f.calls())
		}
	}
}

func TestOpenConversation_TemplateRejected(t *testing.T) {
	cases := []scriptedReply{
		{status: http.StatusBadRequest, body: `{"error":"x","reason":"template_parameters","expected":2,"received":1}`},
		{status: http.StatusConflict, body: `{"error":"x","reason":"template_not_offerable"}`},
		{status: http.StatusConflict, body: `{"error":"x","reason":"template_other_account"}`},
		{status: http.StatusBadGateway, body: `{"error":"x","reason":"meta_rejected","meta_code":132000}`},
	}
	for _, reply := range cases {
		f := newFakeOpen(t, reply)
		c, _ := newOpenClient(f)
		_, err := c.OpenConversation(context.Background(), 7, OpenRecipient{Phone: "573001112233"}, openTmpl, "")
		if !errors.Is(err, ErrTemplateRejected) || !errors.Is(err, bird.ErrPermanentSend) {
			t.Fatalf("%d %s: quiero ErrTemplateRejected permanente, tengo %v", reply.status, reply.body, err)
		}
		if f.calls() != 1 {
			t.Fatalf("%s: %d llamadas", reply.body, f.calls())
		}
	}
}

func TestOpenConversation_IdempotencyReused(t *testing.T) {
	f := newFakeOpen(t, scriptedReply{status: http.StatusConflict, body: `{"error":"x","reason":"idempotency_key_reused"}`})
	c, _ := newOpenClient(f)
	_, err := c.OpenConversation(context.Background(), 7, OpenRecipient{Phone: "573001112233"}, openTmpl, "")
	if !errors.Is(err, ErrIdempotencyReused) || f.calls() != 1 {
		t.Fatalf("err=%v llamadas=%d", err, f.calls())
	}
}

func TestOpenConversation_404(t *testing.T) {
	// En la apertura el 404 NO es «ya no es mía»: es «canal o BSUID ajeno». Confundirlo con
	// ErrNotMine haría que quien llama soltara un caso que nunca tuvo.
	f := newFakeOpen(t, scriptedReply{status: http.StatusNotFound})
	c, _ := newOpenClient(f)
	_, err := c.OpenConversation(context.Background(), 7, OpenRecipient{Phone: "573001112233"}, openTmpl, "")
	if err == nil || errors.Is(err, ErrNotMine) {
		t.Fatalf("el 404 de la apertura no es ErrNotMine: %v", err)
	}
	if !errors.Is(err, ErrOpenNotFound) || !errors.Is(err, bird.ErrPermanentSend) {
		t.Fatalf("quiero ErrOpenNotFound permanente: %v", err)
	}
	if f.calls() != 1 {
		t.Fatalf("%d llamadas", f.calls())
	}
}

func TestOpenConversation_NotConfigured(t *testing.T) {
	c := NewClient("", "", "")
	if _, err := c.OpenConversation(context.Background(), 7, OpenRecipient{Phone: "1"}, openTmpl, ""); !errors.Is(err, ErrNotConfiguredAPI) {
		t.Fatalf("err = %v", err)
	}
}

// --- El puente ---

func TestBridgeOpenWithTemplate(t *testing.T) {
	f := newFakeOpen(t, scriptedReply{status: http.StatusCreated, body: opened201})
	c, _ := newOpenClient(f)
	b := NewBridge(c, map[string]string{"proj-recordatorio": "recordatorio_cita"})
	b.ChannelID = 7

	convID, msgID, err := b.OpenWithTemplate(context.Background(), "573001112233", bird.TemplateConfig{
		ProjectID: "proj-recordatorio",
		Locale:    "es_CO",
		Params: []bird.TemplateParam{
			{Type: "string", Key: "1", Value: "Ana"},
			{Type: "string", Key: "2", Value: "18 de septiembre"},
		},
	})
	if err != nil || convID != 77 || msgID != "901" {
		t.Fatalf("conv=%d msg=%q err=%v", convID, msgID, err)
	}
	var sent struct {
		ChannelID int64             `json:"channel_id"`
		Recipient map[string]string `json:"recipient"`
		Template  TemplateBody      `json:"template"`
	}
	_ = json.Unmarshal(f.bodies[0], &sent)
	// El idioma es el MISMO que el de SendTemplate (el Locale tal cual): un solo traductor.
	want := TemplateBody{
		Name: "recordatorio_cita", Language: "es_CO",
		Parameters: map[string]string{"1": "Ana", "2": "18 de septiembre"},
	}
	if sent.ChannelID != 7 || sent.Recipient["phone"] != "573001112233" || !reflect.DeepEqual(sent.Template, want) {
		t.Fatalf("cuerpo: %s", f.bodies[0])
	}
}

func TestBridgeOpenWithTemplate_AlreadyOpenPassesThrough(t *testing.T) {
	f := newFakeOpen(t, scriptedReply{
		status: http.StatusConflict,
		body:   `{"error":"x","reason":"already_open","conversation_id":55,"mine":true}`,
	})
	c, _ := newOpenClient(f)
	b := NewBridge(c, map[string]string{"p": "plantilla_x"})
	b.ChannelID = 7
	_, _, err := b.OpenWithTemplate(context.Background(), "573001112233", bird.TemplateConfig{ProjectID: "p"})
	var already *AlreadyOpenError
	if !errors.As(err, &already) || already.ConversationID != 55 || !already.Mine {
		t.Fatalf("err = %v", err)
	}
}

func TestBridgeOpenWithTemplate_WithoutChannelOrNameSendsNothing(t *testing.T) {
	f := newFakeOpen(t)
	c, _ := newOpenClient(f)

	b := NewBridge(c, map[string]string{"p": "plantilla_x"})
	_, _, err := b.OpenWithTemplate(context.Background(), "573001112233", bird.TemplateConfig{ProjectID: "p"})
	if !errors.Is(err, ErrOpenNotConfigured) || !errors.Is(err, bird.ErrPermanentSend) {
		t.Fatalf("sin canal: %v", err)
	}

	b.ChannelID = 7
	b.TemplateNames = nil
	if _, _, err := b.OpenWithTemplate(context.Background(), "573001112233", bird.TemplateConfig{ProjectID: "p"}); err == nil {
		t.Fatal("sin nombre de Meta la apertura falla")
	}
	if f.calls() != 0 {
		t.Fatalf("nada sale a Néctar: %d llamadas", f.calls())
	}
}

func TestBridgeContactAndWindow(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	b := NewBridge(NewClient(srv.URL, svcKey, reqSecret), nil)

	body = `{"state":"bot","window_open":true,"window_expires_at":null,` +
		`"contact":{"identifier":"","bsuid":"CO.abc","profile_name":"Ana"}}`
	got, err := b.Contact(context.Background(), 1234)
	if err != nil || got != (bird.NectarContact{BSUID: "CO.abc"}) {
		t.Fatalf("contact=%+v err=%v", got, err)
	}
	open, err := b.WindowOpen(context.Background(), 1234)
	if err != nil || !open {
		t.Fatalf("window=%v err=%v", open, err)
	}

	body = `{"state":"bot","window_open":false,"window_expires_at":null}`
	got, err = b.Contact(context.Background(), 1234)
	if err != nil || got != (bird.NectarContact{}) {
		t.Fatalf("sin bloque el contacto va vacío: %+v err=%v", got, err)
	}
	if open, _ := b.WindowOpen(context.Background(), 1234); open {
		t.Fatal("ventana cerrada")
	}
}

// Con el puente puesto pero sin canal de apertura (NECTAR_CHANNEL_ID=0), la plantilla proactiva
// falla de forma permanente y nada sale: ni a Néctar ni a Bird.
func TestSendProactiveTemplate_NoChannel(t *testing.T) {
	f := newFakeOpen(t)
	c, _ := newOpenClient(f)
	b := NewBridge(c, map[string]string{"p": "plantilla_x"})

	var birdCalls int
	birdSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		birdCalls++
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(birdSrv.Close)
	bc := bird.NewClientForTest(birdSrv.URL)
	bc.SetNectar(b)

	_, err := bc.SendProactiveTemplate(context.Background(), "+573001112233", bird.TemplateConfig{ProjectID: "p"})
	if !errors.Is(err, ErrOpenNotConfigured) || !errors.Is(err, bird.ErrPermanentSend) {
		t.Fatalf("sin canal: %v", err)
	}
	if f.calls() != 0 || birdCalls != 0 {
		t.Fatalf("ninguna llamada HTTP: néctar=%d bird=%d", f.calls(), birdCalls)
	}
}

// Un fallo de transporte con la petición ya enviada (conexión cortada, timeout) está en duda: la
// plantilla pudo salir. No se reintenta, ni aquí ni con otra clave en quien llama.
func TestOpenConversation_TransportErrorIsInDoubt(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("sin hijacker")
		}
		conn, _, _ := hj.Hijack()
		_ = conn.Close() // corta la conexión sin responder
	}))
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL, svcKey, reqSecret)
	_, err := c.OpenConversation(context.Background(), 7, OpenRecipient{Phone: "573001112233"}, openTmpl, "")
	if !errors.Is(err, ErrOpenInDoubt) || !bird.IsPermanentSendError(err) {
		t.Fatalf("quiero ErrOpenInDoubt permanente, tengo %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("en duda NO se reintenta: %d llamadas", calls.Load())
	}
}

// Sin configuración no sale nada: eso NO está en duda.
func TestOpenConversation_NotConfiguredIsNotInDoubt(t *testing.T) {
	_, err := NewClient("", "", "").OpenConversation(context.Background(), 7, OpenRecipient{Phone: "573001112233"}, openTmpl, "")
	if err == nil || errors.Is(err, ErrOpenInDoubt) {
		t.Fatalf("sin configurar no hay duda: %v", err)
	}
}

// countingTransport cuenta las peticiones que el cliente intenta, lleguen o no a un servidor.
type countingTransport struct{ calls atomic.Int32 }

func (c *countingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c.calls.Add(1)
	return http.DefaultTransport.RoundTrip(r)
}

// Conexión rechazada (puerto cerrado): la petición nunca llegó a Néctar, así que NO está en duda.
// Sale como error transitorio (ni ErrOpenInDoubt ni permanente) y sin reintento aquí.
func TestOpenConversation_DialErrorIsTransient(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close() // puerto cerrado: connection refused

	c := NewClient("http://"+addr, svcKey, reqSecret)
	rt := &countingTransport{}
	c.http = &http.Client{Transport: rt, Timeout: 5 * time.Second}
	_, err = c.OpenConversation(context.Background(), 7, OpenRecipient{Phone: "573001112233"}, openTmpl, "")
	if err == nil || errors.Is(err, ErrOpenInDoubt) || bird.IsPermanentSendError(err) {
		t.Fatalf("quiero un error transitorio, tengo %v", err)
	}
	var op *net.OpError
	if !errors.As(err, &op) || op.Op != "dial" {
		t.Fatalf("la causa (dial) se conserva: %v", err)
	}
	if n := rt.calls.Load(); n != 1 {
		t.Fatalf("exactamente una llamada: %d", n)
	}
}

func TestNotSent(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"dial", &net.OpError{Op: "dial", Err: errors.New("connection refused")}, true},
		{"dns", &net.DNSError{Err: "no such host", Name: "nectar.invalid"}, true},
		{"read tras conectar", &net.OpError{Op: "read", Err: errors.New("connection reset")}, false},
		{"eof", io.EOF, false},
	}
	for _, tc := range cases {
		if got := notSent(&transportError{err: tc.err}); got != tc.want {
			t.Errorf("%s: notSent = %v, quiero %v", tc.name, got, tc.want)
		}
	}
}
