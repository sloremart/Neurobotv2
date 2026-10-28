package bird

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
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

// untouched dice si el puente no vio NINGUNA llamada.
func (f *fakeBridge) untouched() bool {
	return f.texts+f.buttons+f.lists+f.templates+f.availableAsks+len(f.escalations)+
		len(f.closed)+len(f.stateAsks) == 0
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

func TestRealPhonesStayOnBird(t *testing.T) {
	// Un teléfono real nunca desvía: el cliente de Bird hace su camino (el servidor de mentira
	// lo ve) y el puente no ve NADA. Es lo que deja intacto el camino de Bird con el puente puesto.
	c, fb, stub := newBridgedClient(t)
	_, _ = c.SendText("+573001234567", "", "hola")
	_, _ = c.SendText("+573001234567", "1a2b3c4d-uuid-de-bird", "hola")
	_, _ = c.SendInternalText("+573001234567", "1a2b3c4d-uuid-de-bird", "nota")
	_ = c.CloseFeedItems("+573001234567", "1a2b3c4d-uuid-de-bird")
	_ = c.TagConversation("+573001234567", "1a2b3c4d-uuid-de-bird", "tag")
	if !fb.untouched() {
		t.Fatalf("un teléfono real nunca va a Néctar: %+v", fb)
	}
	if len(stub.requests()) == 0 {
		t.Fatal("el camino de Bird no se ejecutó: la prueba no prueba nada")
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

func TestWithoutBridgeNothingChanges(t *testing.T) {
	srv, stub := newBirdStub(t)
	c := NewClientForTest(srv.URL) // c.nectar == nil
	if _, ok := c.nectarConversation("nectar:42", "42"); ok {
		t.Fatal("sin puente no hay desvío")
	}
	// «nectar:42» no es E.164: sin puente sigue el camino de Bird para contactos sin teléfono
	// (resolver el BSUID por PATCH), que aquí contesta 404 → no contactable.
	_, err := c.SendText("nectar:42", "", "hola")
	if !errors.Is(err, ErrNonContactable) {
		t.Fatalf("sin puente SendText sigue el camino de Bird (BSUID, sin contacto): %v", err)
	}
	reqs := stub.requests()
	if len(reqs) == 0 || !strings.HasPrefix(reqs[0], "PATCH ") || !strings.Contains(reqs[0], "/contacts/identifiers/") {
		t.Fatalf("esperaba el PATCH de resolución de BSUID de Bird, got %v", reqs)
	}
	if id, err := c.CreateConversationForPhone(context.Background(), "nectar:77"); err == nil && id == "77" {
		t.Fatal("sin puente CreateConversationForPhone no devuelve la clave como conversación")
	}
	if id, _ := c.LookupConversationByPhone("nectar:77"); id == "77" {
		t.Fatal("sin puente LookupConversationByPhone no devuelve la clave como conversación")
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
