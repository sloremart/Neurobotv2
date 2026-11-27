package bird

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// El desvío a Néctar dentro del cliente de Bird: las conversaciones nectar:* van al puente;
// todo lo demás sigue por Bird exactamente como antes del puente. Se cuenta a dónde fue cada
// llamada: el doble del puente registra lo que recibe y el servidor de Bird registra lo que le
// llega, de modo que «no fue a Bird» y «no fue a Néctar» se afirman con hechos, no por ausencia.

var errNotMine = errors.New("404 not mine")

type escalateCall struct {
	id  int64
	esc Escalation
}

type fakeBridge struct {
	texts, buttons, lists, templates int
	documents                        []documentCall
	textIDs                          []int64
	templateIDs                      []int64
	escalations                      []escalateCall
	closed                           []int64
	available                        bool
	availableAsks                    int
	availableErr                     error
	closeErr                         error
	state                            string  // el estado crudo que devuelve GET …/state
	stateErr                         error   //
	stateAsks                        []int64 // a qué conversaciones se les preguntó
	// Guion de la apertura: openID/openMsg son el 201; openErr, el rechazo. Sin guion, falla.
	openID     int64
	openMsg    string
	openErr    error
	openPhones []string // a qué teléfonos se intentó abrir
}

func (f *fakeBridge) SendText(_ context.Context, id int64, _ string) (string, error) {
	f.texts++
	f.textIDs = append(f.textIDs, id)
	return "n-1", nil
}

func (f *fakeBridge) SendButtons(_ context.Context, _ int64, _ string, b []Button) (string, error) {
	f.buttons++
	for _, btn := range b {
		if len([]rune(btn.Text)) > waButtonLabelMax {
			return "", errors.New("título sin truncar")
		}
	}
	return "n-2", nil
}

func (f *fakeBridge) SendList(_ context.Context, _ int64, _, _ string, s []ListSection) (string, error) {
	f.lists++
	for _, sec := range s {
		if len([]rune(sec.Title)) > waSectionTitle {
			return "", errors.New("título de sección sin truncar")
		}
		for _, r := range sec.Rows {
			if len([]rune(r.Title)) > waRowTitleMax {
				return "", errors.New("fila sin truncar")
			}
			if len([]rune(r.Description)) > waRowDescMax {
				return "", errors.New("descripción de fila sin truncar")
			}
		}
	}
	return "n-3", nil
}

func (f *fakeBridge) SendTemplate(_ context.Context, id int64, _ TemplateConfig) (string, error) {
	f.templates++
	f.templateIDs = append(f.templateIDs, id)
	return "n-4", nil
}

func (f *fakeBridge) Available(_ context.Context, _ int64) (bool, string, error) {
	f.availableAsks++
	return f.available, "x", f.availableErr
}

func (f *fakeBridge) Escalate(_ context.Context, id int64, esc Escalation) error {
	f.escalations = append(f.escalations, escalateCall{id: id, esc: esc})
	return nil
}

func (f *fakeBridge) Close(_ context.Context, id int64) error {
	f.closed = append(f.closed, id)
	return f.closeErr
}

func (f *fakeBridge) ConversationState(_ context.Context, id int64) (string, error) {
	f.stateAsks = append(f.stateAsks, id)
	return f.state, f.stateErr
}

func (f *fakeBridge) IsNotMine(err error) bool { return errors.Is(err, errNotMine) }

func (f *fakeBridge) OpenWithTemplate(_ context.Context, phone string, _ TemplateConfig) (int64, string, error) {
	f.openPhones = append(f.openPhones, phone)
	if f.openErr != nil {
		return 0, "", f.openErr
	}
	if f.openID == 0 {
		return 0, "", errors.New("fakeBridge: OpenWithTemplate sin guion")
	}
	return f.openID, f.openMsg, nil
}

func (f *fakeBridge) Contact(context.Context, int64) (NectarContact, error) {
	return NectarContact{}, nil
}

func (f *fakeBridge) WindowOpen(context.Context, int64) (bool, error) { return false, nil }

func (f *fakeBridge) SendDocument(_ context.Context, id int64, filename string, _ []byte, caption string) (string, error) {
	f.documents = append(f.documents, documentCall{id: id, filename: filename, caption: caption})
	return "doc-1", nil
}

type documentCall struct {
	id                int64
	filename, caption string
}

// untouched dice si el puente no vio NINGUNA llamada.
func (f *fakeBridge) untouched() bool {
	return f.texts+f.buttons+f.lists+f.templates+f.availableAsks+len(f.escalations)+
		len(f.closed)+len(f.stateAsks)+len(f.openPhones)+len(f.documents) == 0
}

// birdStub es el Bird de mentira: cuenta cada petición que le llega y contesta 404 a todo
// (un 404 es permanente: nada reintenta ni duerme). requests() devuelve "MÉTODO ruta".
type birdStub struct {
	mu   sync.Mutex
	reqs []string
}

func (s *birdStub) record(r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reqs = append(s.reqs, r.Method+" "+r.URL.Path)
}

func (s *birdStub) requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.reqs...)
}

func newBirdStub(t *testing.T) (*httptest.Server, *birdStub) {
	t.Helper()
	stub := &birdStub{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stub.record(r)
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"code":"NotFound"}`))
	}))
	t.Cleanup(srv.Close)
	return srv, stub
}

// newBridgedClient es un cliente con el puente puesto cuyo Bird es el servidor de mentira.
func newBridgedClient(t *testing.T) (*Client, *fakeBridge, *birdStub) {
	t.Helper()
	srv, stub := newBirdStub(t)
	c := NewClientForTest(srv.URL)
	fb := &fakeBridge{}
	c.SetNectar(fb)
	return c, fb, stub
}

func TestNectarConversation_OnlySessionKeyDetours(t *testing.T) {
	c, _, _ := newBridgedClient(t)
	cases := []struct {
		name           string
		phone, convID  string
		wantID         int64
		wantRoutedToNc bool
	}{
		{"clave de sesión con su conversation_id", "nectar:42", "42", 42, true},
		{"clave de sesión sin conversation_id", "nectar:42", "", 42, true},
		{"teléfono real con id numérico NO desvía", "+573001112233", "42", 0, false},
		{"teléfono vacío con id numérico NO desvía", "", "42", 0, false},
		{"id cero no es una conversación", "nectar:0", "", 0, false},
		{"id negativo no es una conversación", "nectar:-5", "", 0, false},
		{"id que no es número", "nectar:x", "", 0, false},
		{"prefijo sin id", "nectar:", "", 0, false},
	}
	for _, tc := range cases {
		id, ok := c.nectarConversation(tc.phone, tc.convID)
		if ok != tc.wantRoutedToNc || id != tc.wantID {
			t.Errorf("%s: nectarConversation(%q,%q) = (%d,%v), quiero (%d,%v)",
				tc.name, tc.phone, tc.convID, id, ok, tc.wantID, tc.wantRoutedToNc)
		}
	}

	// Sin cliente o sin puente nunca hay desvío, ni siquiera con la clave de sesión.
	var nilClient *Client
	if _, ok := nilClient.nectarConversation("nectar:42", "42"); ok {
		t.Error("un *Client nil no desvía")
	}
	if _, ok := NewClientForTest("http://unused").nectarConversation("nectar:42", "42"); ok {
		t.Error("sin puente no hay desvío")
	}
}

// Descuadre entre la clave de sesión y el conversation_id: manda la clave, pero queda dicho.
func TestNectarConversation_MismatchLogsWarnAndKeyWins(t *testing.T) {
	c, _, _ := newBridgedClient(t)
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(prev)

	id, ok := c.nectarConversation("nectar:42", "99")
	if !ok || id != 42 {
		t.Fatalf("manda la clave de sesión: id=%d ok=%v", id, ok)
	}
	if out := buf.String(); !strings.Contains(out, "level=WARN") || !strings.Contains(out, "no coinciden") {
		t.Fatalf("el descuadre debe quedar como WARN, got %q", out)
	}

	buf.Reset()
	if _, ok := c.nectarConversation("nectar:42", "42"); !ok {
		t.Fatal("clave y conversation_id coinciden: desvía")
	}
	if _, ok := c.nectarConversation("nectar:42", ""); !ok {
		t.Fatal("conversation_id vacío: desvía")
	}
	if buf.Len() != 0 {
		t.Fatalf("sin descuadre no se avisa de nada: %q", buf.String())
	}
}

// Con un id numérico SUELTO (sin la clave de sesión) tampoco se desvía: un id numérico puede
// venir de cualquier parte (arnés de pruebas, datos viejos), la clave solo la arma el webhook de Néctar.
func TestANumericConversationIDAloneIsNotEnough(t *testing.T) {
	c, fb, stub := newBridgedClient(t)
	_ = c.CloseFeedItems("", "1234")
	_, _ = c.SendInternalText("", "1234", "resumen")
	_, _ = c.SendText("573001112233", "4711", "hola")
	if !fb.untouched() {
		t.Fatalf("nada de esto pudo llegar a Néctar: %+v", fb)
	}
	if len(stub.requests()) == 0 {
		t.Fatal("el camino de Bird no se ejecutó: la prueba no prueba nada")
	}
}

// Sin puente no hay desvío: una clave nectar:* tampoco cae al camino de Bird (antes hacía el PATCH
// de BSUID con whatsappusername=nectar:42 y creaba conversaciones): no tiene canal y lo dice con
// ErrNectarBridgeOff. Método por método lo fija TestNectarConversationNeverTouchesBirdWithBridgeOff.
func TestWithoutBridgeNectarKeyHasNoChannel(t *testing.T) {
	srv, stub := newBirdStub(t)
	c := NewClientForTest(srv.URL) // c.nectar == nil
	if _, ok := c.nectarConversation("nectar:42", "42"); ok {
		t.Fatal("sin puente no hay desvío")
	}
	if _, err := c.SendText("nectar:42", "", "hola"); !errors.Is(err, ErrNectarBridgeOff) {
		t.Fatalf("sin puente una conversación de Néctar no tiene canal: %v", err)
	}
	if id, err := c.CreateConversationForPhone(context.Background(), "nectar:77"); err == nil || id == "77" {
		t.Fatal("sin puente CreateConversationForPhone no devuelve la clave como conversación")
	}
	if id, _ := c.LookupConversationByPhone("nectar:77"); id == "77" {
		t.Fatal("sin puente LookupConversationByPhone no devuelve la clave como conversación")
	}
	if reqs := stub.requests(); len(reqs) != 0 {
		t.Fatalf("nada de esto llega a Bird: %v", reqs)
	}
}

func TestSendText_NectarRoutesBeforeBsuid(t *testing.T) {
	c, fb, stub := newBridgedClient(t)
	// «nectar:42» NO es E.164: el desvío tiene que ir ANTES de la rama del BSUID, que pagaría
	// un PATCH a Bird por una clave que no es un contacto.
	id, err := c.SendText("nectar:42", "", "hola")
	if err != nil || id != "n-1" {
		t.Fatalf("id=%q err=%v", id, err)
	}
	if fb.texts != 1 || len(fb.textIDs) != 1 || fb.textIDs[0] != 42 {
		t.Fatalf("el texto va al puente con el id de la clave: %+v", fb)
	}
	if reqs := stub.requests(); len(reqs) != 0 {
		t.Fatalf("Bird no debía recibir nada (ni sendViaBsuid): %v", reqs)
	}
}

func TestSendText_NectarRespectsRateLimit(t *testing.T) {
	// El tope por destinatario se evalúa ANTES del desvío: una sesión nectar:* desbocada se
	// corta igual que un teléfono de Bird.
	c, fb, _ := newBridgedClient(t)
	c.sendRateLimit = 1
	if _, err := c.SendText("nectar:42", "42", "uno"); err != nil {
		t.Fatalf("el primero pasa: %v", err)
	}
	if _, err := c.SendText("nectar:42", "42", "dos"); !errors.Is(err, ErrSendRateLimited) {
		t.Fatalf("el segundo se corta por tope: %v", err)
	}
	if fb.texts != 1 {
		t.Fatalf("al puente solo llegó el primero: texts=%d", fb.texts)
	}
}

func TestNectarSessionsAreRoutedToTheBridge(t *testing.T) {
	c, fb, stub := newBridgedClient(t)
	long := "Un título de botón demasiado largo para WhatsApp"
	if id, err := c.SendText("nectar:1234", "1234", "hola"); err != nil || id != "n-1" {
		t.Fatalf("id=%q err=%v", id, err)
	}
	if _, err := c.SendButtons("nectar:1234", "1234", "elige", []Button{{Text: long, Payload: "p"}}); err != nil {
		t.Fatalf("SendButtons debe truncar el título antes de pasarlo al puente: %v", err)
	}
	if _, err := c.SendList("nectar:1234", "1234", "b", "Ver", []ListSection{
		{Title: long + long, Rows: []ListRow{{ID: "r", Title: long, Description: long + long}}},
	}); err != nil {
		t.Fatalf("SendList debe truncar secciones y filas antes de pasarlas al puente: %v", err)
	}
	if _, err := c.SendTemplate("nectar:1234", TemplateConfig{}); err != nil {
		t.Fatal(err)
	}
	if fb.texts != 1 || fb.buttons != 1 || fb.lists != 1 || fb.templates != 1 {
		t.Fatalf("cada envío de una sesión nectar:* va al puente: %+v", fb)
	}
	if reqs := stub.requests(); len(reqs) != 0 {
		t.Fatalf("y ninguno toca Bird: %v", reqs)
	}
}

// El kill switch WHATSAPP_NOTIFICATIONS_ENABLED=false significa «no sale NINGUNA plantilla»; se usa
// en local para proteger teléfonos reales. Gana al puente: se evalúa ANTES del desvío.
func TestSendTemplate_KillSwitchBeatsNectar(t *testing.T) {
	c := &Client{notifWADisabled: true}
	fb := &fakeBridge{}
	c.SetNectar(fb)
	id, err := c.SendTemplate("nectar:42", TemplateConfig{ProjectID: "p"})
	if !errors.Is(err, ErrWhatsAppNotificationsDisabled) {
		t.Fatalf("esperaba ErrWhatsAppNotificationsDisabled, got %v", err)
	}
	if id != "" {
		t.Errorf("con el interruptor apagado no hay id: %q", id)
	}
	if fb.templates != 0 || !fb.untouched() {
		t.Fatalf("el puente no debía ver llamada alguna: %+v", fb)
	}
}

func TestSendTemplate_NectarUsesTheKeyID(t *testing.T) {
	c, fb, _ := newBridgedClient(t)
	if id, err := c.SendTemplate("nectar:42", TemplateConfig{ProjectID: "p"}); err != nil || id != "n-4" {
		t.Fatalf("id=%q err=%v", id, err)
	}
	if len(fb.templateIDs) != 1 || fb.templateIDs[0] != 42 {
		t.Fatalf("la plantilla va a la conversación de la clave: %v", fb.templateIDs)
	}
}

// Néctar no tiene nota interna, ni desasignar, ni etiquetar, ni renombrar la conversación:
// todo eso es un no-op que NO toca Bird (con la clave por teléfono, aunque conversationID venga).
func TestNectarKeyedNoOpsNeverReachBird(t *testing.T) {
	c, fb, stub := newBridgedClient(t)
	if id, err := c.SendInternalText("nectar:1234", "1234", "resumen"); err != nil || id != "" {
		t.Fatalf("la nota interna no tiene destino en Néctar: id=%q err=%v", id, err)
	}
	if err := c.UnassignFeedItem("nectar:1234", "1234", false); err != nil {
		t.Fatalf("unassign no existe en Néctar: %v", err)
	}
	if err := c.TagConversation("nectar:1234", "1234", "tag"); err != nil {
		t.Fatalf("tag no existe en Néctar: %v", err)
	}
	if err := c.MarkConversationEscalated("nectar:1234", "1234", "Equipo", "Paciente"); err != nil {
		t.Fatalf("renombrar la conversación no existe en Néctar: %v", err)
	}
	if !fb.untouched() {
		t.Fatalf("y tampoco hay llamada al puente: %+v", fb)
	}
	if reqs := stub.requests(); len(reqs) != 0 {
		t.Fatalf("Bird no debía recibir nada: %v", reqs)
	}
}

func TestFetchMessageText_EmptyIDWithBridgeNeverReachesBird(t *testing.T) {
	c, _, stub := newBridgedClient(t)
	if got := c.FetchMessageText(""); got != "" {
		t.Fatalf("got %q", got)
	}
	if reqs := stub.requests(); len(reqs) != 0 {
		t.Fatalf("Bird no debía recibir nada: %v", reqs)
	}
}

func TestCloseFeedItems_NectarCallsClose(t *testing.T) {
	c, fb, stub := newBridgedClient(t)
	if err := c.CloseFeedItems("nectar:1234", "1234"); err != nil {
		t.Fatal(err)
	}
	if len(fb.closed) != 1 || fb.closed[0] != 1234 {
		t.Fatalf("closed=%v", fb.closed)
	}
	if reqs := stub.requests(); len(reqs) != 0 {
		t.Fatalf("Bird no debía recibir nada: %v", reqs)
	}
}

func TestCloseFeedItems_NectarNotMineIsSuccess(t *testing.T) {
	c, fb, _ := newBridgedClient(t)
	fb.closeErr = errNotMine
	if err := c.CloseFeedItems("nectar:1234", "1234"); err != nil {
		t.Fatalf("404 al cerrar = ya no era nuestra = objetivo cumplido, got %v", err)
	}
	if len(fb.closed) != 1 {
		t.Fatalf("el cierre sí se intentó: %v", fb.closed)
	}
}

func TestCloseFeedItems_NectarOtherErrorPropagates(t *testing.T) {
	c, fb, _ := newBridgedClient(t)
	boom := errors.New("503 de Néctar")
	fb.closeErr = boom
	if err := c.CloseFeedItems("nectar:1234", "1234"); !errors.Is(err, boom) {
		t.Fatalf("un error que no es «no es mía» se propaga: %v", err)
	}
}

func TestLookupOfANectarSessionIsTheKeyItself(t *testing.T) {
	c, _, stub := newBridgedClient(t)
	if id, err := c.LookupConversationByPhone("nectar:77"); err != nil || id != "77" {
		t.Fatalf("id=%q err=%v", id, err)
	}
	if id, err := c.CreateConversationForPhone(context.Background(), "nectar:77"); err != nil || id != "77" {
		t.Fatalf("id=%q err=%v", id, err)
	}
	if reqs := stub.requests(); len(reqs) != 0 {
		t.Fatalf("Bird no debía recibir nada: %v", reqs)
	}
}

func TestAvailabilityIsPerConversationInNectar(t *testing.T) {
	c, fb, _ := newBridgedClient(t)
	fb.available = true
	if !c.HasAvailableAgentsFor("nectar:5", "5") {
		t.Fatal("Néctar dijo que sí")
	}
	fb.available = false
	if c.HasAvailableAgentsFor("nectar:5", "5") {
		t.Fatal("«no se puede escalar» es respuesta normal y bloquea el gate")
	}
	fb.available, fb.availableErr = true, errors.New("timeout")
	if c.HasAvailableAgentsFor("nectar:5", "5") {
		t.Fatal("un fallo de transporte cuenta como «no hay»: nunca se crea una escalación destinada a no-show")
	}
	if fb.availableAsks != 3 {
		t.Fatalf("las tres preguntas fueron al puente: %d", fb.availableAsks)
	}
}

func TestAvailabilityForARealPhoneAsksBirdNotNectar(t *testing.T) {
	c, fb, stub := newBridgedClient(t)
	if c.HasAvailableAgentsFor("+573001234567", "1a2b3c4d-uuid-de-bird") {
		t.Fatal("el Bird de mentira contesta 404: no hay asesores")
	}
	if fb.availableAsks != 0 {
		t.Fatalf("un teléfono de Bird no le pregunta disponibilidad a Néctar: %d", fb.availableAsks)
	}
	if len(stub.requests()) == 0 {
		t.Fatal("la pregunta tenía que ir a Bird")
	}
}

// El estado de la conversación solo lo publica Néctar: una conversación de Bird contesta
// ErrNoConversationState, y quien pregunta lo trata como cualquier otro error (no sabe, no decide).
func TestConversationStateOnlyNectarAnswers(t *testing.T) {
	c, fb, _ := newBridgedClient(t)
	fb.state = "assigned"
	got, err := c.ConversationState(context.Background(), "nectar:5", "5")
	if err != nil || got != "assigned" {
		t.Fatalf("estado=%q err=%v", got, err)
	}
	if len(fb.stateAsks) != 1 || fb.stateAsks[0] != 5 {
		t.Fatalf("se le pregunta por LA conversación: %v", fb.stateAsks)
	}
	if _, err := c.ConversationState(context.Background(), "+573001234567", "1a2b3c4d-uuid-de-bird"); !errors.Is(err, ErrNoConversationState) {
		t.Fatalf("en Bird no hay estado que consultar: %v", err)
	}
	if len(fb.stateAsks) != 1 {
		t.Fatalf("y la pregunta no sale hacia Néctar: %v", fb.stateAsks)
	}
}

func TestEscalateToAgentWithSummary_NectarSingleCall(t *testing.T) {
	c, fb, stub := newBridgedClient(t)
	team := int64(7)
	want := Escalation{
		Protocol:        1,
		Reason:          "patient_asked_for_human",
		ReasonDetail:    "escalation_reason",
		Summary:         "RESUMEN",
		Facts:           []byte(`{"paciente":{}}`),
		ReturnMenu:      []byte(`{"paso_actual":"MAIN_MENU","menu":[]}`),
		Input:           []byte(`{"step":"MAIN_MENU"}`),
		ResolveLock:     "cita_pendiente",
		SuggestedTeamID: &team,
		Rejection:       &Rejection{Detail: "motivo", Attempt: 2, Echo: "eco", Field: "campo", Piece: "pieza"},
	}
	agentID, agentName, err := c.EscalateToAgentWithSummary(context.Background(), "5", "nectar:5",
		"team", "Equipo", "Paciente", "fallback", want)
	if err != nil || agentID != "" || agentName != "" {
		t.Fatalf("agent=%q/%q err=%v", agentID, agentName, err)
	}
	// UNA llamada, con el resumen DENTRO y el sobre entero tal cual: después de escalar el bot
	// ya no alcanza la conversación, así que no hay nota interna aparte.
	if len(fb.escalations) != 1 {
		t.Fatalf("una sola llamada a Escalate: %d", len(fb.escalations))
	}
	if got := fb.escalations[0]; got.id != 5 || !reflect.DeepEqual(got.esc, want) {
		t.Fatalf("el sobre viaja entero al puente con el id de la clave:\n got=%+v\nwant=%+v", got, want)
	}
	if reqs := stub.requests(); len(reqs) != 0 {
		t.Fatalf("ni nota interna ni asignación en Bird: %v", reqs)
	}
}

func TestEscalateToAgentWithSummary_NectarErrorPropagates(t *testing.T) {
	c, fb, _ := newBridgedClient(t)
	c.SetNectar(&failingEscalateBridge{fakeBridge: fb})
	_, _, err := c.EscalateToAgentWithSummary(context.Background(), "5", "nectar:5",
		"team", "Equipo", "Paciente", "fallback", Escalation{Summary: "RESUMEN"})
	if !errors.Is(err, errEscalate) {
		t.Fatalf("el error del puente llega al caller: %v", err)
	}
}

var errEscalate = errors.New("escalate falló")

type failingEscalateBridge struct{ *fakeBridge }

func (failingEscalateBridge) Escalate(context.Context, int64, Escalation) error { return errEscalate }

func TestEscalateToAgentWithSummary_BirdSendsInternalNote(t *testing.T) {
	var mu sync.Mutex
	var order []string
	var noteBody map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == "PATCH" && r.URL.Path == "/workspaces/ws-test/conversations/conv-1":
			order = append(order, "mark")
			w.WriteHeader(200)
		case r.Method == "GET" && r.URL.Path == "/workspaces/ws-test/agents":
			order = append(order, "agents")
			w.WriteHeader(200)
			_, _ = w.Write([]byte(agentsJSON(AgentInfo{
				ID: "agent-1", DisplayName: "Agente Uno",
				Teams:        []AgentTeam{{ID: "team-a", Name: "A"}},
				Availability: AgentAvailability{Status: "active", Activity: "available"},
			})))
		case isFeedItemSearch(r):
			order = append(order, "search")
			w.WriteHeader(200)
			_, _ = w.Write([]byte(feedItemSearchJSON("conv-1")))
		case r.Method == "PATCH" && r.URL.Path == "/workspaces/ws-test/feeds/channel:ch-test/items/fi-conv-1":
			order = append(order, "assign")
			w.WriteHeader(200)
		case r.Method == "POST" && r.URL.Path == "/workspaces/ws-test/conversations/conv-1/messages":
			order = append(order, "note")
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &noteBody)
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"id":"msg-note"}`))
		default:
			t.Errorf("petición inesperada: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	// Con el puente PUESTO pero un teléfono real: el desvío no aplica, la escalación es la de
	// siempre (asignar y luego la nota interna) y el puente no ve nada — tampoco el candado del
	// botón «Resolver», que es una guarda del servidor de Néctar sin equivalente en el Inbox de Bird.
	c := NewClientForTest(srv.URL)
	fb := &fakeBridge{}
	c.SetNectar(fb)
	agentID, agentName, err := c.EscalateToAgentWithSummary(context.Background(), "conv-1", "+573001234567",
		"team-a", "Equipo", "Paciente", "fallback", Escalation{
			Reason: "patient_asked_for_human", Summary: "RESUMEN", ResolveLock: "cita_pendiente",
		})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if agentID != "agent-1" || agentName != "Agente Uno" {
		t.Fatalf("Bird devuelve el agente asignado: %q/%q", agentID, agentName)
	}
	if want := []string{"mark", "agents", "search", "assign", "note"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("EscalateToAgent y DESPUÉS la nota interna: got %v want %v", order, want)
	}
	if noteBody["draft"] != true {
		t.Errorf("la nota va como borrador (solo Inbox, no al paciente): %v", noteBody)
	}
	textBody, _ := noteBody["body"].(map[string]interface{})
	textField, _ := textBody["text"].(map[string]interface{})
	if textField["text"] != "RESUMEN" {
		t.Errorf("la nota lleva el resumen: %v", noteBody)
	}
	if !fb.untouched() {
		t.Fatalf("un teléfono de Bird nunca escala por el puente: %+v", fb)
	}
}

func TestEscalateToAgentWithSummary_BirdEscalateErrorSkipsNote(t *testing.T) {
	srv, stub := newBirdStub(t)
	c := NewClientForTest(srv.URL)
	// Sin conversación ni teléfono EscalateToAgent falla de plano con «empty conversation ID»:
	// no se manda nota interna a ninguna parte.
	_, _, err := c.EscalateToAgentWithSummary(context.Background(), "", "", "team", "Equipo", "Paciente", "fb",
		Escalation{Summary: "RESUMEN"})
	if err == nil {
		t.Fatal("esperaba el error de EscalateToAgent")
	}
	if reqs := stub.requests(); len(reqs) != 0 {
		t.Fatalf("sin escalación no hay nota: %v", reqs)
	}
}

// Con el conversationID VACÍO, EscalateToAgent resuelve la conversación por su cuenta (caché,
// lookup) y escala bien; la nota con el resumen tiene que ir a esa misma conversación y no
// perderse (antes salía después de que el handler recuperaba el id de la caché).
func TestEscalateToAgentWithSummary_BirdEmptyConversationIDUsesCachedForNote(t *testing.T) {
	var mu sync.Mutex
	var notes []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == "PATCH" && r.URL.Path == "/workspaces/ws-test/conversations/conv-1":
			w.WriteHeader(200)
		case r.Method == "GET" && r.URL.Path == "/workspaces/ws-test/agents":
			w.WriteHeader(200)
			_, _ = w.Write([]byte(agentsJSON(AgentInfo{
				ID: "agent-1", DisplayName: "Agente Uno",
				Teams:        []AgentTeam{{ID: "team-a", Name: "A"}},
				Availability: AgentAvailability{Status: "active", Activity: "available"},
			})))
		case isFeedItemSearch(r):
			w.WriteHeader(200)
			_, _ = w.Write([]byte(feedItemSearchJSON("conv-1")))
		case r.Method == "PATCH" && r.URL.Path == "/workspaces/ws-test/feeds/channel:ch-test/items/fi-conv-1":
			w.WriteHeader(200)
		case r.Method == "POST" && r.URL.Path == "/workspaces/ws-test/conversations/conv-1/messages":
			notes = append(notes, r.URL.Path)
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"id":"msg-note"}`))
		default:
			t.Errorf("petición inesperada: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	c := NewClientForTest(srv.URL)
	c.CacheConversationID("+573001234567", "conv-1")
	if _, _, err := c.EscalateToAgentWithSummary(context.Background(), "", "+573001234567",
		"team-a", "Equipo", "Paciente", "fallback", Escalation{Summary: "RESUMEN"}); err != nil {
		t.Fatalf("err=%v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(notes) != 1 {
		t.Fatalf("la nota con el resumen va a la conversación resuelta: %v", notes)
	}
}

// tripwireBridge es un puente que NO debe sonar: cualquier llamada falla la prueba en el acto.
// Sirve para fijar la transición con dos líneas activas: con el puente encendido, una sesión de
// Bird (clave = teléfono) se queda entera en Bird.
type tripwireBridge struct{ t *testing.T }

func (b tripwireBridge) trip(method string) {
	b.t.Helper()
	b.t.Errorf("una sesión de Bird llamó al puente de Néctar: %s", method)
}

func (b tripwireBridge) SendText(context.Context, int64, string) (string, error) {
	b.trip("SendText")
	return "", nil
}

func (b tripwireBridge) SendButtons(context.Context, int64, string, []Button) (string, error) {
	b.trip("SendButtons")
	return "", nil
}

func (b tripwireBridge) SendList(context.Context, int64, string, string, []ListSection) (string, error) {
	b.trip("SendList")
	return "", nil
}

func (b tripwireBridge) SendTemplate(context.Context, int64, TemplateConfig) (string, error) {
	b.trip("SendTemplate")
	return "", nil
}

func (b tripwireBridge) Available(context.Context, int64) (bool, string, error) {
	b.trip("Available")
	return false, "", nil
}

func (b tripwireBridge) Escalate(context.Context, int64, Escalation) error {
	b.trip("Escalate")
	return nil
}

func (b tripwireBridge) Close(context.Context, int64) error {
	b.trip("Close")
	return nil
}

func (b tripwireBridge) ConversationState(context.Context, int64) (string, error) {
	b.trip("ConversationState")
	return "", nil
}

func (b tripwireBridge) IsNotMine(error) bool {
	b.trip("IsNotMine")
	return false
}

func (b tripwireBridge) OpenWithTemplate(context.Context, string, TemplateConfig) (int64, string, error) {
	b.trip("OpenWithTemplate")
	return 0, "", nil
}

func (b tripwireBridge) Contact(context.Context, int64) (NectarContact, error) {
	b.trip("Contact")
	return NectarContact{}, nil
}

func (b tripwireBridge) SendDocument(context.Context, int64, string, []byte, string) (string, error) {
	b.trip("SendDocument")
	return "", nil
}

func (b tripwireBridge) WindowOpen(context.Context, int64) (bool, error) {
	b.trip("WindowOpen")
	return false, nil
}

// keyRoutedCase es un método de *Client con rama Néctar, llamado con una clave de sesión dada. La
// tabla la comparten la prueba de la sesión de Bird (puente encendido y apagado) y la de la
// conversación de Néctar con el puente apagado: la lista de métodos es UNA.
type keyRoutedCase struct {
	name string
	call func(c *Client, convID string) error
	// route es la petición de Bird que el método hace con una clave de Bird, por conversation_id
	// ("" = ninguna).
	route map[string]string
	// returnsErr: el método devuelve error (el que no, se llama igual y se ignora su cero).
	returnsErr bool
	// keyless: el método no recibe la clave (FetchMessageText va por id de mensaje de Bird).
	keyless bool
}

// birdTestConvUUID es un conversation_id de Bird para las tablas de rutas.
const birdTestConvUUID = "1a2b3c4d-uuid-de-bird"

// keyRoutedCases arma la tabla para la clave dada.
func keyRoutedCases(key string) []keyRoutedCase {
	const (
		uuid = birdTestConvUUID
		ws   = "/workspaces/ws-test"
	)
	channelMsg := "POST " + ws + "/channels/ch-test/messages"
	convMsg := "POST " + ws + "/conversations/" + uuid + "/messages"
	feedSearch := "POST " + ws + "/search/feed-items"
	return []keyRoutedCase{
		{name: "SendText", returnsErr: true, call: func(c *Client, id string) error {
			_, err := c.SendText(key, id, "hola")
			return err
		}, route: map[string]string{"": channelMsg, uuid: convMsg}},
		{name: "SendButtons", returnsErr: true, call: func(c *Client, id string) error {
			_, err := c.SendButtons(key, id, "elige", []Button{{Text: "Sí", Payload: "si"}})
			return err
		}, route: map[string]string{"": channelMsg, uuid: convMsg}},
		{name: "SendList", returnsErr: true, call: func(c *Client, id string) error {
			_, err := c.SendList(key, id, "b", "Ver", []ListSection{{Title: "s", Rows: []ListRow{{ID: "r", Title: "fila"}}}})
			return err
		}, route: map[string]string{"": channelMsg, uuid: convMsg}},
		{name: "SendInternalText", returnsErr: true, call: func(c *Client, id string) error {
			_, err := c.SendInternalText(key, id, "nota")
			return err
		}, route: map[string]string{uuid: convMsg}},
		{name: "SendTemplate", returnsErr: true, call: func(c *Client, _ string) error {
			_, err := c.SendTemplate(key, TemplateConfig{ProjectID: "p"})
			return err
		}, route: map[string]string{"": channelMsg, uuid: channelMsg}},
		{name: "EscalateToAgentWithSummary", returnsErr: true, call: func(c *Client, id string) error {
			// Plazo corto: el primer intento de asignar sí llega a Bird; los reintentos (1 s, 2 s, 3 s)
			// no añaden nada a lo que se prueba.
			escCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			_, _, err := c.EscalateToAgentWithSummary(escCtx, id, key, "team", "Equipo", "Paciente", "fallback",
				Escalation{Reason: "patient_asked_for_human", Summary: "RESUMEN", ResolveLock: "cita_pendiente"})
			return err
		}, route: map[string]string{"": "GET " + ws + "/conversations", uuid: "PATCH " + ws + "/conversations/" + uuid}},
		{name: "MarkConversationEscalated", returnsErr: true, call: func(c *Client, id string) error {
			return c.MarkConversationEscalated(key, id, "Equipo", "Paciente")
		}, route: map[string]string{uuid: "PATCH " + ws + "/conversations/" + uuid}},
		{name: "UnassignFeedItem (retorno al bot)", returnsErr: true, call: func(c *Client, id string) error {
			return c.UnassignFeedItem(key, id, false)
		}, route: map[string]string{uuid: feedSearch}},
		{name: "UnassignFeedItem cerrando", returnsErr: true, call: func(c *Client, id string) error {
			return c.UnassignFeedItem(key, id, true)
		}, route: map[string]string{uuid: feedSearch}},
		{name: "CloseFeedItems", returnsErr: true, call: func(c *Client, id string) error {
			return c.CloseFeedItems(key, id)
		}, route: map[string]string{uuid: feedSearch}},
		{name: "TagConversation", returnsErr: true, call: func(c *Client, id string) error {
			return c.TagConversation(key, id, "tag")
		}, route: map[string]string{uuid: "POST " + ws + "/conversations/" + uuid + "/tags"}},
		{name: "HasAvailableAgentsFor", call: func(c *Client, id string) error {
			_ = c.HasAvailableAgentsFor(key, id)
			return nil
		}, route: map[string]string{"": "GET " + ws + "/agents", uuid: "GET " + ws + "/agents"}},
		{name: "LookupConversationByPhone", returnsErr: true, call: func(c *Client, _ string) error {
			_, err := c.LookupConversationByPhone(key)
			return err
		}, route: map[string]string{"": "GET " + ws + "/conversations", uuid: "GET " + ws + "/conversations"}},
		{name: "CreateConversationForPhone", returnsErr: true, call: func(c *Client, _ string) error {
			_, err := c.CreateConversationForPhone(context.Background(), key)
			return err
		}, route: map[string]string{"": "POST " + ws + "/conversations", uuid: "POST " + ws + "/conversations"}},
		{name: "SendDocumentCtx", returnsErr: true, call: func(c *Client, _ string) error {
			_, err := c.SendDocumentCtx(context.Background(), key, "r.pdf", []byte("x"), "pie")
			return err
		}, route: map[string]string{}},
		{name: "FetchMessageText", keyless: true, call: func(c *Client, _ string) error {
			_ = c.FetchMessageText("msg-1")
			return nil
		}, route: map[string]string{
			"": "GET " + ws + "/channels/ch-test/messages/msg-1", uuid: "GET " + ws + "/channels/ch-test/messages/msg-1",
		}},
	}
}

// Con el puente ENCENDIDO, cada método de *Client que tiene rama Néctar (todos los que pasan por
// nectarConversation; la lista sale de sus llamadores, no de memoria) deja una llamada con la clave de
// Bird en Bird, y se afirma por método con dos hechos: el puente no suena (tripwireBridge) y el Bird
// falso recibe la ruta PROPIA de ese método. La ruta es lo que distingue el camino de Bird de las
// ramas Néctar que son no-op sin llamar al puente (nota interna, marcar escalada, desasignar,
// etiquetar, lookup, crear conversación): ahí el tripwire solo no prueba nada.
//
// Sin conversation_id, la nota interna, el marcado, el desasignar, el cierre y la etiqueta no llaman
// a nada en Bird (salen antes por falta de id), así que en esa vuelta solo los guarda el tripwire;
// la vuelta con el UUID de Bird es la que los fija por su ruta.
//
// Con el puente APAGADO la clave de Bird sigue exactamente el mismo camino (vuelta puente=false): la
// guarda de la vuelta a Bird solo mira claves nectar:*.
//
// SendProactiveTemplate NO está: con el puente encendido abre SIEMPRE por Néctar a propósito (todo
// envío proactivo sale por la línea nueva), y eso lo fijan sus propias pruebas.
func TestBirdSessionNeverTouchesNectarWithBridgeOn(t *testing.T) {
	const phone = "+573001112233"
	for _, bridgeOn := range []bool{true, false} {
		for _, convID := range []string{"", birdTestConvUUID} {
			for _, tc := range keyRoutedCases(phone) {
				t.Run(fmt.Sprintf("%s/conv=%q/puente=%v", tc.name, convID, bridgeOn), func(t *testing.T) {
					srv, stub := newBirdStub(t)
					c := NewClientForTest(srv.URL)
					if bridgeOn {
						c.SetNectar(tripwireBridge{t: t})
					}
					_ = tc.call(c, convID)
					reqs := stub.requests()
					want, ok := tc.route[convID]
					if !ok {
						// Sin id, el camino de Bird sale antes de llamar a nada; el tripwire es la guarda.
						if len(reqs) != 0 {
							t.Errorf("sin conversation_id no esperaba peticiones a Bird: %v", reqs)
						}
						return
					}
					if !slices.Contains(reqs, want) {
						t.Errorf("la clave de Bird tenía que llegar a %q en Bird; llegó %v", want, reqs)
					}
				})
			}
		}
	}

	// Las consultas que solo publica Néctar: con la clave de Bird no se pregunta nada (tripwire) y la
	// respuesta dice «no es de Néctar».
	c := NewClientForTest("http://unused")
	c.SetNectar(tripwireBridge{t: t})
	ctx := context.Background()
	if _, err := c.ConversationState(ctx, phone, birdTestConvUUID); !errors.Is(err, ErrNoConversationState) {
		t.Errorf("una sesión de Bird no tiene estado en Néctar: %v", err)
	}
	if _, ok := c.NectarWindowOpen(ctx, phone); ok {
		t.Error("la ventana de una sesión de Bird no se le pregunta a Néctar")
	}
	if _, ok, _ := c.NectarContact(ctx, phone); ok {
		t.Error("el contacto de una sesión de Bird no se le pregunta a Néctar")
	}
}

// Con el puente APAGADO (vuelta a Bird: NECTAR_BASE_URL vacía), una conversación de Néctar ya no
// tiene canal: cada método de la misma tabla, con la clave "nectar:55", hace CERO peticiones a Bird
// —ni el upsert de contacto con whatsappusername=nectar:55, ni crear o asignar una conversación—, y
// los que devuelven error devuelven ErrNectarBridgeOff, que es permanente (nadie lo reintenta). El
// conversation_id se prueba vacío, con el id de Néctar y con un UUID de Bird: ninguno lo cambia.
//
// FetchMessageText no recibe la clave (va por id de mensaje de Bird, que un mensaje de Néctar no
// tiene): para él se prueba que un mensaje sin id no llega a Bird.
func TestNectarConversationNeverTouchesBirdWithBridgeOff(t *testing.T) {
	const key = "nectar:55"
	for _, convID := range []string{"", "55", birdTestConvUUID} {
		for _, tc := range keyRoutedCases(key) {
			if tc.keyless {
				continue
			}
			t.Run(fmt.Sprintf("%s/conv=%q", tc.name, convID), func(t *testing.T) {
				srv, stub := newBirdStub(t)
				c := NewClientForTest(srv.URL)
				err := tc.call(c, convID)
				if reqs := stub.requests(); len(reqs) != 0 {
					t.Errorf("sin puente, una conversación de Néctar no puede tocar Bird: %v", reqs)
				}
				if !tc.returnsErr {
					return
				}
				if !errors.Is(err, ErrNectarBridgeOff) || !IsPermanentSendError(err) {
					t.Errorf("esperaba ErrNectarBridgeOff permanente, got %v", err)
				}
			})
		}
	}

	t.Run("FetchMessageText sin id", func(t *testing.T) {
		srv, stub := newBirdStub(t)
		c := NewClientForTest(srv.URL)
		if got := c.FetchMessageText(""); got != "" {
			t.Errorf("sin id no hay texto, got %q", got)
		}
		if reqs := stub.requests(); len(reqs) != 0 {
			t.Errorf("un mensaje sin id no llega a Bird: %v", reqs)
		}
	})

	t.Run("SendProactiveTemplate", func(t *testing.T) {
		srv, stub := newBirdStub(t)
		c := NewClientForTest(srv.URL)
		sent, err := c.SendProactiveTemplate(context.Background(), key, TemplateConfig{ProjectID: "p"})
		if reqs := stub.requests(); len(reqs) != 0 {
			t.Errorf("sin puente, la plantilla a una conversación de Néctar no puede tocar Bird: %v", reqs)
		}
		if !errors.Is(err, ErrNectarBridgeOff) || !IsPermanentSendError(err) {
			t.Errorf("esperaba ErrNectarBridgeOff permanente, got %v", err)
		}
		if sent != (ProactiveSend{}) {
			t.Errorf("sin envío no hay resultado, got %+v", sent)
		}
	})

	t.Run("HasAvailableAgentsFor", func(t *testing.T) {
		c := NewClientForTest("http://unused")
		if c.HasAvailableAgentsFor(key, "") {
			t.Error("sin puente no hay a quién escalar una conversación de Néctar")
		}
	})

	t.Run("ConversationState", func(t *testing.T) {
		c := NewClientForTest("http://unused")
		if _, err := c.ConversationState(context.Background(), key, ""); !errors.Is(err, ErrNectarBridgeOff) {
			t.Errorf("esperaba ErrNectarBridgeOff, got %v", err)
		}
	})
}

func TestBirdClient_SendDocumentCtx_BirdPhoneNotSupported(t *testing.T) {
	fb := &fakeBridge{}
	c := NewClientForTest("http://unused")
	c.SetNectar(fb)
	_, err := c.SendDocumentCtx(context.Background(), "+573001112233", "r.pdf", []byte("x"), "")
	if !errors.Is(err, ErrDocumentNotSupported) || !IsPermanentSendError(err) {
		t.Fatalf("esperaba ErrDocumentNotSupported permanente, got %v", err)
	}
	if len(fb.documents) != 0 {
		t.Fatalf("un teléfono de Bird no llega al puente: %v", fb.documents)
	}
}

func TestBirdClient_SendDocumentCtx_NectarKeyGoesToBridge(t *testing.T) {
	fb := &fakeBridge{}
	c := NewClientForTest("http://unused")
	c.SetNectar(fb)
	id, err := c.SendDocumentCtx(context.Background(), "nectar:42", "r.pdf", []byte("x"), "pie")
	if err != nil || id != "doc-1" {
		t.Fatalf("id=%q err=%v", id, err)
	}
	if len(fb.documents) != 1 || fb.documents[0] != (documentCall{id: 42, filename: "r.pdf", caption: "pie"}) {
		t.Fatalf("documentos: %+v", fb.documents)
	}
}
