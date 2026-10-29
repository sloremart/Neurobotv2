package handlers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/config"
	"github.com/neuro-bot/neuro-bot/internal/nectar"
	"github.com/neuro-bot/neuro-bot/internal/observability"
	"github.com/neuro-bot/neuro-bot/internal/session"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
)

// La escalación por el puente de Néctar: el sobre (v0 / v1), el equipo sugerido por grupo de
// CUPS, el aviso al paciente ANTES del escalate y la cola del próximo día hábil. El camino de
// Bird (sesión E.164) queda como hoy, y una prueba de este archivo lo fija.

// fakeNectarBridge registra, en orden, lo que el handler le pide al puente.
type fakeNectarBridge struct {
	mu          sync.Mutex
	events      []string // "text:<mensaje>" y "escalate", en el orden real
	escalations []bird.Escalation
	unavailable bool
	escalateErr error
}

func (b *fakeNectarBridge) SendText(_ context.Context, _ int64, text string) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = append(b.events, "text:"+text)
	return "m1", nil
}

func (b *fakeNectarBridge) SendButtons(_ context.Context, _ int64, text string, _ []bird.Button) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = append(b.events, "buttons:"+text)
	return "m2", nil
}

func (b *fakeNectarBridge) SendList(_ context.Context, _ int64, body, _ string, _ []bird.ListSection) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = append(b.events, "list:"+body)
	return "m3", nil
}

func (b *fakeNectarBridge) SendTemplate(context.Context, int64, bird.TemplateConfig) (string, error) {
	return "m4", nil
}

func (b *fakeNectarBridge) Available(context.Context, int64) (bool, string, error) {
	if b.unavailable {
		return false, "fuera del horario de atención", nil
	}
	return true, "", nil
}

func (b *fakeNectarBridge) Escalate(_ context.Context, _ int64, esc bird.Escalation) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = append(b.events, "escalate")
	b.escalations = append(b.escalations, esc)
	return b.escalateErr
}

func (b *fakeNectarBridge) Close(context.Context, int64) error { return nil }

func (b *fakeNectarBridge) ConversationState(context.Context, int64) (string, error) {
	return "waiting", nil
}

func (b *fakeNectarBridge) IsNotMine(error) bool { return false }

func (b *fakeNectarBridge) OpenWithTemplate(context.Context, string, bird.TemplateConfig) (int64, string, error) {
	return 0, "", nil
}

func (b *fakeNectarBridge) Contact(context.Context, int64) (bird.NectarContact, error) {
	return bird.NectarContact{}, nil
}

func (b *fakeNectarBridge) WindowOpen(context.Context, int64) (bool, error) { return false, nil }

// texts devuelve los textos que el puente sacó al paciente, en orden.
func (b *fakeNectarBridge) texts() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for _, ev := range b.events {
		if s, ok := strings.CutPrefix(ev, "text:"); ok {
			out = append(out, s)
		}
	}
	return out
}

// nectarMachine arma la máquina de escalación con el puente doble.
func nectarMachine(t *testing.T, cfg *config.Config, escRepo EscalationCreator) (*sm.Machine, *fakeNectarBridge) {
	t.Helper()
	client := bird.NewClientForTest("http://nectar.invalid")
	nb := &fakeNectarBridge{}
	client.SetNectar(nb)
	m := sm.NewMachine()
	RegisterEscalationHandlers(m, client, cfg, escRepo)
	return m, nb
}

// nectarSession es una sesión de Néctar en ESCALATE_TO_AGENT. askedForAgent = el paciente
// pidió la persona (palabra clave u opción de menú).
func nectarSession(askedForAgent bool) *session.Session {
	s := testSess(sm.StateEscalateToAgent)
	s.PhoneNumber = "nectar:12"
	s.ConversationID = "12"
	if askedForAgent {
		s.SetContext(sm.CtxPatientAskedForAgent, "1")
	}
	return s
}

func nectarMsg() bird.InboundMessage {
	return bird.InboundMessage{Phone: "nectar:12", ConversationID: "12", Text: "agente"}
}

func withProtocolV1(cfg *config.Config) *config.Config {
	cfg.ProtocolEscaladoVersion = config.ProtocolEscaladoV1
	return cfg
}

// nextDayConfig es el horario REAL con el margen y la espera por defecto: TestingAlwaysOpen va
// apagado porque estas pruebas van del reloj.
func nextDayConfig() *config.Config {
	cfg := testEscalationConfig()
	cfg.TestingAlwaysOpen = false
	cfg.BusinessHoursCloseMarginMin = 15
	cfg.BusinessHoursQueueMaxWaitMin = 1200
	return cfg
}

// escalateOnce corre una escalación por el puente y devuelve el resultado.
func escalateOnce(t *testing.T, m *sm.Machine, sess *session.Session) *sm.StateResult {
	t.Helper()
	res, err := m.Process(context.Background(), sess, nectarMsg())
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func hasClear(r *sm.StateResult, key string) bool {
	for _, k := range r.ClearCtx {
		if k == key {
			return true
		}
	}
	return false
}

func TestEscalate_NectarEnvelopeV1(t *testing.T) {
	cfg := withProtocolV1(testEscalationConfig())
	cfg.NectarTeamByGroup = map[string]int64{"imagenes": 7}
	m, nb := nectarMachine(t, cfg, nil)
	sess := nectarSession(true)
	sess.SetContext("cups_code", "883100")
	sess.SetContext("escalation_reason", "ocr_fallido")

	escalateOnce(t, m, sess)

	if len(nb.escalations) != 1 {
		t.Fatalf("se esperaba UNA escalación, hubo %d", len(nb.escalations))
	}
	esc := nb.escalations[0]
	if esc.Protocol != config.ProtocolEscaladoV1 {
		t.Errorf("Protocol = %d, want 1", esc.Protocol)
	}
	if esc.Reason != nectar.ReasonPatientAskedForHuman {
		t.Errorf("Reason = %q, want %q", esc.Reason, nectar.ReasonPatientAskedForHuman)
	}
	if esc.ReasonDetail != "ocr_fallido" {
		t.Errorf("ReasonDetail = %q: el detalle es el escalation_reason interno", esc.ReasonDetail)
	}
	if esc.Facts == nil {
		t.Error("con v1 los hechos viajan")
	}
	var groups []map[string]any
	if err := json.Unmarshal(esc.Facts, &groups); err != nil || len(groups) == 0 {
		t.Errorf("facts tiene que ser una lista de grupos: %v (%s)", err, esc.Facts)
	}
	if esc.Input == nil {
		t.Error("con v1 el descriptor de entrada viaja")
	}
	if esc.ReturnMenu != nil {
		t.Errorf("con v1 el menú del dialecto no viaja: %s", esc.ReturnMenu)
	}
	if esc.SuggestedTeamID == nil || *esc.SuggestedTeamID != 7 {
		t.Errorf("SuggestedTeamID = %v, want 7 (grupo imagenes)", esc.SuggestedTeamID)
	}
	if !strings.Contains(esc.Summary, "Transferencia de chatbot") {
		t.Errorf("el resumen de texto viaja siempre: %q", esc.Summary)
	}
}

// Sin la marca del paciente, el motivo es que el bot no pudo responder.
func TestEscalate_NectarReasonBotCannotAnswer(t *testing.T) {
	m, nb := nectarMachine(t, testEscalationConfig(), nil)
	escalateOnce(t, m, nectarSession(false))
	if len(nb.escalations) != 1 || nb.escalations[0].Reason != nectar.ReasonBotCannotAnswer {
		t.Fatalf("motivo: %+v", nb.escalations)
	}
}

func TestEscalate_V0EnvelopeKeepsReturnMenu(t *testing.T) {
	cfg := testEscalationConfig()
	m, nb := nectarMachine(t, cfg, nil)
	sess := nectarSession(true)
	sess.SetContext("escalation_reason", "ocr_fallido")

	escalateOnce(t, m, sess)

	if len(nb.escalations) != 1 {
		t.Fatalf("se esperaba UNA escalación, hubo %d", len(nb.escalations))
	}
	esc := nb.escalations[0]
	if esc.Protocol != 0 {
		t.Errorf("Protocol = %d, want 0", esc.Protocol)
	}
	if esc.Input != nil {
		t.Errorf("el dialecto no lleva descriptor: %s", esc.Input)
	}
	if esc.ReturnMenu == nil {
		t.Error("el dialecto lleva el menú de retornos")
	}
	if esc.Facts != nil {
		t.Errorf("el dialecto no lleva hechos: %s", esc.Facts)
	}
	if esc.ReasonDetail != "" {
		t.Errorf("el dialecto no lleva detalle del motivo: %q", esc.ReasonDetail)
	}
	if esc.SuggestedTeamID != nil {
		t.Errorf("sin NECTAR_TEAM_BY_GROUP no se sugiere equipo: %v", *esc.SuggestedTeamID)
	}
}

func TestEscalate_NectarNoticeSentBeforeEscalate(t *testing.T) {
	m, nb := nectarMachine(t, testEscalationConfig(), nil)
	res := escalateOnce(t, m, nectarSession(true))

	if len(nb.events) != 2 || nb.events[0] != "text:"+sm.EscalationNoticeText || nb.events[1] != "escalate" {
		t.Fatalf("el aviso va ANTES del escalate y una sola vez: %v", nb.events)
	}
	for _, msg := range res.Messages {
		if sm.OutboundText(msg) == sm.EscalationNoticeText {
			t.Fatal("el aviso no se repite en el resultado: saldría después del escalate")
		}
	}
}

// Si el pre-envío ya sacó el aviso del caller (gancho de 7b), el handler no manda otro.
func TestEscalate_NectarCallerNoticeAlreadyFlushedIsNotRepeated(t *testing.T) {
	m, nb := nectarMachine(t, testEscalationConfig(), nil)
	sess := nectarSession(true)
	sess.SetContext(sm.CtxEscalationNoticeFlushed, "1")

	escalateOnce(t, m, sess)

	if len(nb.events) != 1 || nb.events[0] != "escalate" {
		t.Fatalf("con el aviso del caller ya enviado, solo se escala: %v", nb.events)
	}
}

// countInternalNotes sustituye el envío de la nota de comandos por un contador mientras corre fn.
func countInternalNotes(t *testing.T, fn func()) int {
	t.Helper()
	prev := sendAgentCommandsNote
	defer func() { sendAgentCommandsNote = prev }()
	calls := 0
	sendAgentCommandsNote = func(*bird.Client, string, string, string) { calls++ }
	fn()
	return calls
}

// En Néctar no hay nota interna: el handler no pide la de comandos.
func TestEscalate_NectarNoAgentCommandsNote(t *testing.T) {
	m, nb := nectarMachine(t, testEscalationConfig(), nil)
	calls := countInternalNotes(t, func() {
		escalateOnce(t, m, nectarSession(true))
	})
	if len(nb.escalations) != 1 {
		t.Fatalf("se esperaba UNA escalación: %v", nb.events)
	}
	if calls != 0 {
		t.Fatalf("en Néctar no se manda la nota de comandos: %d llamadas", calls)
	}
}

// Y en Bird sí, una vez (el control del contador).
func TestEscalate_BirdSendsAgentCommandsNoteOnce(t *testing.T) {
	rec := &birdRecorder{}
	srv := rec.server(t)
	m := sm.NewMachine()
	RegisterEscalationHandlers(m, bird.NewClientForTest(srv.URL), testEscalationConfig(), nil)
	sess := testSess(sm.StateEscalateToAgent)
	sess.ConversationID = "conv-test"
	calls := countInternalNotes(t, func() {
		if _, err := m.Process(context.Background(), sess, bird.InboundMessage{Phone: "+573001234567", ConversationID: "conv-test"}); err != nil {
			t.Fatal(err)
		}
	})
	if calls != 1 {
		t.Fatalf("en Bird la nota de comandos sale una vez: %d", calls)
	}
}

// La marca del paciente se escribe en el turno de la palabra clave y se borra en el mismo turno
// al escalar: por Process, el resultado final la lleva en ClearCtx y NO en UpdateCtx (si no,
// SaveState la borraría y la volvería a escribir, y quedaría pegada para la siguiente escalación).
func TestEscalate_KeywordMarkClearedThroughProcess(t *testing.T) {
	m, nb := nectarMachine(t, testEscalationConfig(), nil)
	m.AddInterceptor(sm.EscalationKeywordsInterceptor())
	sess := testSess(sm.StateAskDocument)
	sess.PhoneNumber = "nectar:12"
	sess.ConversationID = "12"

	res, err := m.Process(context.Background(), sess, nectarMsg())
	if err != nil {
		t.Fatal(err)
	}
	if res.NextState != sm.StateEscalated || len(nb.escalations) != 1 ||
		nb.escalations[0].Reason != nectar.ReasonPatientAskedForHuman {
		t.Fatalf("la palabra clave escala con el motivo del paciente: %s %+v", res.NextState, nb.escalations)
	}
	if _, kept := res.UpdateCtx[sm.CtxPatientAskedForAgent]; kept {
		t.Fatalf("la marca no puede volver en UpdateCtx: %v", res.UpdateCtx)
	}
	if !hasClear(res, sm.CtxPatientAskedForAgent) {
		t.Fatalf("la marca se borra: %v", res.ClearCtx)
	}
}

// El chequeo de la recuperación IA mira el rechazo de ESTA vuelta (que escalationExit borra), no
// el contador de la sesión: pasado el rechazo, la IA vuelve a estar disponible.
func TestEscalate_RecoveryAvailableAgainAfterRejectionCleared(t *testing.T) {
	client := bird.NewClientForTest("http://nectar.invalid")
	client.SetNectar(&fakeNectarBridge{})
	m := sm.NewMachine()
	rec := &spyRecovery{}
	m.SetRecoveryCoordinator(rec)
	RegisterEscalationHandlers(m, client, testEscalationConfig(), nil)

	sess := nectarSession(false)
	sess.SetContext(sm.CtxAgentInputRejected, "1") // contador de un rechazo ANTERIOR
	escalateOnce(t, m, sess)
	if !rec.called {
		t.Fatal("sin rechazo en curso la recuperación IA se intenta")
	}
}

func TestEscalationSuppressedWhenPatientDidNotAskAndNotQueued(t *testing.T) {
	m, nb := nectarMachine(t, testEscalationConfig(), nil)
	res := escalateOnce(t, m, nectarSession(false))

	if len(nb.escalations) != 1 {
		t.Fatalf("se escala igual: %v", nb.events)
	}
	if txt := nb.texts(); len(txt) != 0 {
		t.Fatalf("el bot atascado no le anuncia un agente al paciente: %v", txt)
	}
	if len(res.Messages) != 0 {
		t.Fatalf("ni por el resultado: %v", res.Messages)
	}
}

// encoladaMotivo devuelve el motivo del evento encolada_proximo_dia, si se emitió.
func encoladaMotivo(sink *observability.CaptureSink) (string, bool) {
	ev := sink.FindStep("escalacion", "encolada_proximo_dia")
	if ev == nil {
		return "", false
	}
	if ev.Attrs == nil {
		return "", true
	}
	m, _ := ev.Attrs["motivo"].(string)
	return m, true
}

func TestEscalate_OutOfHoursQueuesWhenOpenIsNear(t *testing.T) {
	escRepo := &mockEscalationCreator{}
	m, nb := nectarMachine(t, nextDayConfig(), escRepo)
	sess := nectarSession(true)
	sink := &observability.CaptureSink{}

	var res *sm.StateResult
	withTime(at(2026, 10, 23, 17, 50), func() { // viernes, 10 min antes del cierre
		stop := observability.StartCapture(sink)
		res = escalateOnce(t, m, sess)
		stop()
	})

	if res.NextState != sm.StateEscalated || escRepo.calls != 1 || len(nb.escalations) != 1 {
		t.Fatalf("el caso se escala igual: %s / filas=%d / %v", res.NextState, escRepo.calls, nb.events)
	}
	if got := res.UpdateCtx[sm.CtxEscalationNextDay]; got != "1" {
		t.Fatalf("falta la marca de escalación encolada: %q (%v)", got, res.UpdateCtx)
	}
	if hasClear(res, sm.CtxEscalationNextDay) {
		t.Fatalf("la escalación que se encola no borra su propia marca: %v", res.ClearCtx)
	}
	txt := nb.texts()
	if len(txt) != 1 {
		t.Fatalf("el paciente paga UN mensaje: %v", nb.events)
	}
	if txt[0] != escalationNoticeText(true) || !strings.Contains(txt[0], "próximo día hábil") ||
		!strings.Contains(txt[0], BusinessHoursDescription()) {
		t.Fatalf("el aviso es el del próximo día hábil, con el horario: %q", txt[0])
	}
	if nb.events[0] != "text:"+txt[0] || nb.events[1] != "escalate" {
		t.Fatalf("y sale antes del escalate: %v", nb.events)
	}
	motivo, ok := encoladaMotivo(sink)
	if !ok || motivo != "margen" {
		t.Fatalf("evento encolada_proximo_dia: ok=%v motivo=%q", ok, motivo)
	}
	// La sesión vive hasta la apertura del sábado más 8 h (el TTL normal vence de madrugada).
	if want := at(2026, 10, 24, 15, 0); !res.ExtendExpiryUntil.Equal(want) {
		t.Fatalf("ExtendExpiryUntil = %s, want %s", res.ExtendExpiryUntil, want)
	}
}

func TestEscalate_OutOfHoursFarOpenDiverts(t *testing.T) {
	escRepo := &mockEscalationCreator{}
	m, nb := nectarMachine(t, nextDayConfig(), escRepo)
	sess := nectarSession(true)
	sink := &observability.CaptureSink{}

	var res *sm.StateResult
	withTime(at(2026, 10, 24, 13, 0), func() { // sábado 13:00: abre el lunes
		stop := observability.StartCapture(sink)
		res = escalateOnce(t, m, sess)
		stop()
	})

	if escRepo.calls != 0 || len(nb.events) != 0 {
		t.Fatalf("con la apertura lejos NO se escala ni se avisa por el puente: filas=%d %v", escRepo.calls, nb.events)
	}
	if res.NextState != sm.StateMainMenu {
		t.Fatalf("el desvío de siempre devuelve al menú, está en %q", res.NextState)
	}
	if _, queued := res.UpdateCtx[sm.CtxEscalationNextDay]; queued {
		t.Fatal("nada quedó encolado")
	}
	ev := sink.FindStep("escalacion", "escalation_no_agents")
	switch {
	case ev == nil:
		t.Fatal("falta el evento escalation_no_agents")
	case ev.Attrs["motivo"] != queueReasonFarOpen:
		t.Fatalf("motivo = %v, want %s (attrs %v)", ev.Attrs["motivo"], queueReasonFarOpen, ev.Attrs)
	}
	if _, ok := encoladaMotivo(sink); ok {
		t.Fatal("no hay nada encolado que anunciar")
	}
	if len(res.Messages) == 0 || !strings.Contains(sm.OutboundText(res.Messages[0]), "no hay asesores disponibles") {
		t.Fatalf("el texto del desvío es el de siempre: %v", res.Messages)
	}
}

// spyRecovery arranca siempre la recuperación y anota que se le pidió.
type spyRecovery struct{ called bool }

func (s *spyRecovery) Active(*session.Session) bool { return false }
func (s *spyRecovery) Handle(context.Context, *session.Session, bird.InboundMessage) (*sm.StateResult, bool) {
	return nil, false
}

func (s *spyRecovery) TryStart(_ context.Context, _ *session.Session, _ bird.InboundMessage, blockedState string) (*sm.StateResult, bool) {
	s.called = true
	return sm.NewResult(blockedState).WithText("aclaración"), true
}

// Lo que rompió una persona no lo arregla la IA: con un dato de agente rechazado no se intenta
// la recuperación, y el rechazo tipado viaja en el sobre v1 y se borra al salir.
func TestEscalate_AgentInputRejectedSkipsRecoveryAndTravels(t *testing.T) {
	client := bird.NewClientForTest("http://nectar.invalid")
	nb := &fakeNectarBridge{}
	client.SetNectar(nb)
	m := sm.NewMachine()
	rec := &spyRecovery{}
	m.SetRecoveryCoordinator(rec)
	RegisterEscalationHandlers(m, client, withProtocolV1(testEscalationConfig()), nil)

	sess := nectarSession(false)
	sess.SetContext(sm.CtxAgentInputRejected, "1")
	raw, err := sm.EncodeRejection(sm.PersistedRejection{Reason: "documento inválido", Attempt: 1, Echo: "abc"})
	if err != nil {
		t.Fatal(err)
	}
	sess.SetContext(sm.CtxAgentInputRejection, raw)

	res := escalateOnce(t, m, sess)
	if rec.called {
		t.Fatal("la recuperación IA no corre sobre un dato rechazado del agente")
	}
	if len(nb.escalations) != 1 || nb.escalations[0].Rejection == nil ||
		nb.escalations[0].Rejection.Detail != "documento inválido" {
		t.Fatalf("el rechazo tipado viaja en el sobre: %+v", nb.escalations)
	}
	if !hasClear(res, sm.CtxAgentInputRejection) {
		t.Fatalf("el rechazo se borra al salir: %v", res.ClearCtx)
	}
}

// La salida que escala borra la marca del paciente y la nota, que eran de ESTA escalación.
func TestEscalate_ClearsAskedMarkAndNoteOnEscalate(t *testing.T) {
	m, _ := nectarMachine(t, testEscalationConfig(), nil)
	sess := nectarSession(true)
	sess.SetContext("escalation_note", "nota de esta escalación")
	res := escalateOnce(t, m, sess)
	if !hasClear(res, sm.CtxPatientAskedForAgent) || !hasClear(res, "escalation_note") {
		t.Fatalf("se borran la marca y la nota: %v", res.ClearCtx)
	}
}

// El equipo sugerido se anota DESPUÉS de que Néctar aceptó el caso, y solo si hay alguno.
func TestEscalate_SuggestedTeamEventOnlyWhenEscalated(t *testing.T) {
	cfg := testEscalationConfig()
	cfg.NectarTeamByGroup = map[string]int64{"imagenes": 7}

	t.Run("aceptado", func(t *testing.T) {
		m, _ := nectarMachine(t, cfg, nil)
		sess := nectarSession(true)
		sess.SetContext("cups_code", "883100")
		sink := &observability.CaptureSink{}
		stop := observability.StartCapture(sink)
		escalateOnce(t, m, sess)
		stop()
		ev := sink.FindStep("escalacion", "equipo_sugerido")
		switch {
		case ev == nil:
			t.Fatal("falta el evento equipo_sugerido")
		case ev.Attrs["grupo"] != "imagenes":
			t.Fatalf("attrs: %v", ev.Attrs)
		}
	})
	t.Run("rechazado", func(t *testing.T) {
		m, nb := nectarMachine(t, cfg, nil)
		nb.escalateErr = io.ErrUnexpectedEOF
		sess := nectarSession(true)
		sess.SetContext("cups_code", "883100")
		sink := &observability.CaptureSink{}
		stop := observability.StartCapture(sink)
		escalateOnce(t, m, sess)
		stop()
		if sink.FindStep("escalacion", "equipo_sugerido") != nil {
			t.Fatal("un escalado que Néctar no recibió no cuenta equipo sugerido")
		}
	})
}

// birdRecorder es un servidor de Bird que acepta la escalación y registra las notas internas
// (draft:true) y los textos al paciente.
type birdRecorder struct {
	mu           sync.Mutex
	notes        []string
	patientTexts int
}

func (b *birdRecorder) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/workspaces/ws-test/agents":
			_, _ = w.Write([]byte(`{"results":[{"id":"agent-1","displayName":"Test Agent","teams":[{"id":"team-fallback","name":"CC"}],"availability":{"status":"active","activity":"available"},"rootItemAssignedCount":0}]}`))
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/search/feed-items"):
			_, _ = w.Write([]byte(`{"results":[{"id":"fi-conv-test","feedId":"channel:ch-test","closed":false}]}`))
		case r.Method == "POST" && strings.Contains(r.URL.Path, "/conversations/") && strings.HasSuffix(r.URL.Path, "/messages"):
			raw, _ := io.ReadAll(r.Body)
			var body struct {
				Draft bool `json:"draft"`
				Body  struct {
					Text struct {
						Text string `json:"text"`
					} `json:"text"`
				} `json:"body"`
			}
			_ = json.Unmarshal(raw, &body)
			b.mu.Lock()
			if strings.Contains(string(raw), `"draft":true`) {
				b.notes = append(b.notes, body.Body.Text.Text)
			} else {
				b.patientTexts++
			}
			b.mu.Unlock()
			_, _ = w.Write([]byte(`{"id":"msg-ok"}`))
		default:
			_, _ = w.Write([]byte(`{"id":"msg-ok"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// El camino de Bird queda como hoy: nada de aviso propio (viaja en el resultado del caller), la
// nota interna con el resumen y después la de comandos, y ninguna marca de la cola.
func TestEscalate_BirdPathUnchanged(t *testing.T) {
	rec := &birdRecorder{}
	srv := rec.server(t)
	cfg := testEscalationConfig()
	m := sm.NewMachine()
	RegisterEscalationHandlers(m, bird.NewClientForTest(srv.URL), cfg, nil)

	sess := testSess(sm.StateEscalateToAgent)
	sess.ConversationID = "conv-test"
	sess.SetContext("patient_name", "Juan")
	sess.SetContext("cups_code", "883100")
	sess.SetContext("cups_name", "RESONANCIA")
	sess.SetContext(sm.CtxPatientAskedForAgent, "1")

	res, err := m.Process(context.Background(), sess, bird.InboundMessage{Phone: "+573001234567", ConversationID: "conv-test"})
	if err != nil {
		t.Fatal(err)
	}
	if res.NextState != sm.StateEscalated {
		t.Fatalf("expected ESCALATED, got %s", res.NextState)
	}
	if len(res.Messages) != 0 {
		t.Fatalf("en Bird el aviso viaja en el resultado del caller, no en el de este handler: %v", res.Messages)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.patientTexts != 0 {
		t.Fatalf("ningún texto directo al paciente: %d", rec.patientTexts)
	}
	teamName := resolveTeamName(cfg.ResolveTeamForCups("883100"), cfg)
	wantSummary := buildAgentSummary(sess, "883100", teamName)
	wantCommands := buildAgentCommands(sess, "883100")
	if len(rec.notes) != 2 || rec.notes[0] != wantSummary || rec.notes[1] != wantCommands {
		t.Fatalf("notas internas: resumen y comandos, en ese orden:\n%q", rec.notes)
	}
	if _, queued := res.UpdateCtx[sm.CtxEscalationNextDay]; queued {
		t.Fatal("Bird no se encola")
	}
}

// Bird no entra en la cola del próximo día: en el margen escala como hoy y de noche se desvía
// al menú con el texto de hoy.
func TestEscalate_BirdNeverQueues(t *testing.T) {
	t.Run("margen: escala como hoy", func(t *testing.T) {
		rec := &birdRecorder{}
		srv := rec.server(t)
		m := sm.NewMachine()
		RegisterEscalationHandlers(m, bird.NewClientForTest(srv.URL), nextDayConfig(), nil)
		sess := testSess(sm.StateEscalateToAgent)
		sess.ConversationID = "conv-test"
		var res *sm.StateResult
		withTime(at(2026, 10, 23, 17, 50), func() {
			var err error
			res, err = m.Process(context.Background(), sess, bird.InboundMessage{Phone: "+573001234567", ConversationID: "conv-test"})
			if err != nil {
				t.Fatal(err)
			}
		})
		if res.NextState != sm.StateEscalated {
			t.Fatalf("en jornada Bird escala: %s", res.NextState)
		}
		if _, queued := res.UpdateCtx[sm.CtxEscalationNextDay]; queued || !res.ExtendExpiryUntil.IsZero() {
			t.Fatal("Bird no se encola ni estira el vencimiento")
		}
	})
	t.Run("noche: desvío de hoy", func(t *testing.T) {
		rec := &birdRecorder{}
		srv := rec.server(t)
		m := sm.NewMachine()
		RegisterEscalationHandlers(m, bird.NewClientForTest(srv.URL), nextDayConfig(), nil)
		sess := testSess(sm.StateEscalateToAgent)
		sess.ConversationID = "conv-test"
		var res *sm.StateResult
		withTime(at(2026, 10, 27, 18, 30), func() {
			var err error
			res, err = m.Process(context.Background(), sess, bird.InboundMessage{Phone: "+573001234567", ConversationID: "conv-test"})
			if err != nil {
				t.Fatal(err)
			}
		})
		if res.NextState != sm.StateMainMenu {
			t.Fatalf("de noche Bird se desvía al menú: %s", res.NextState)
		}
		want := "En este momento no hay asesores disponibles. 🕐\n\nTe atendemos en horario de *Lunes a Viernes de 7:00 a.m. a 6:00 p.m.* y *Sábados de 7:00 a.m. a 12:00 m.* Por favor escríbenos en ese horario y con gusto te ayudamos. 😊"
		if len(res.Messages) == 0 || sm.OutboundText(res.Messages[0]) != want {
			t.Fatalf("el texto del desvío es el de hoy: %v", res.Messages)
		}
	})
}

// El equipo sugerido es de Néctar: una escalación de Bird no lo anota aunque haya mapa.
func TestEscalate_BirdDoesNotEmitSuggestedTeam(t *testing.T) {
	rec := &birdRecorder{}
	srv := rec.server(t)
	cfg := testEscalationConfig()
	cfg.NectarTeamByGroup = map[string]int64{"imagenes": 7}
	m := sm.NewMachine()
	RegisterEscalationHandlers(m, bird.NewClientForTest(srv.URL), cfg, nil)
	sess := testSess(sm.StateEscalateToAgent)
	sess.ConversationID = "conv-test"
	sess.SetContext("cups_code", "883100")

	sink := &observability.CaptureSink{}
	stop := observability.StartCapture(sink)
	res, err := m.Process(context.Background(), sess, bird.InboundMessage{Phone: "+573001234567", ConversationID: "conv-test"})
	stop()
	if err != nil {
		t.Fatal(err)
	}
	if res.NextState != sm.StateEscalated {
		t.Fatalf("expected ESCALATED, got %s", res.NextState)
	}
	if sink.FindStep("escalacion", "equipo_sugerido") != nil {
		t.Fatal("Bird no anota equipo sugerido")
	}
}

// M4: la marca «el paciente pidió la persona» describe ESTA escalación. Si el paso termina sin
// escalar (tope, desvío del gate, fallo), una marca viva haría que la siguiente escalación —una
// en que el bot se atasca— saliera como pedida por el paciente (motivo y aviso equivocados).
func TestEscalate_AskedMarkClearedOnNonEscalatingExits(t *testing.T) {
	t.Run("tope por sesión", func(t *testing.T) {
		m, _ := nectarMachine(t, testEscalationConfig(), nil)
		sess := nectarSession(true)
		sess.SetContext("escalation_count", "3")
		res := escalateOnce(t, m, sess)
		if res.NextState != sm.StateFallbackMenu || !hasClear(res, sm.CtxPatientAskedForAgent) {
			t.Fatalf("el tope borra la marca: %s %v", res.NextState, res.ClearCtx)
		}
	})
	t.Run("desvío sin asesores", func(t *testing.T) {
		m, nb := nectarMachine(t, nextDayConfig(), nil)
		nb.unavailable = true
		var res *sm.StateResult
		withTime(at(2026, 10, 27, 10, 0), func() {
			res = escalateOnce(t, m, nectarSession(true))
		})
		if res.NextState != sm.StateMainMenu || !hasClear(res, sm.CtxPatientAskedForAgent) {
			t.Fatalf("el desvío borra la marca: %s %v", res.NextState, res.ClearCtx)
		}
	})
}

// M7: una re-escalación por un dato del agente que no sirvió la causó una persona, no un lazo del
// bot: no puede chocar con el tope por sesión (dejaría al paciente sin asesor en la 4.ª vuelta).
func TestEscalate_AgentRejectionReEscalationExemptFromSessionCap(t *testing.T) {
	escRepo := &mockEscalationCreator{}
	m, _ := nectarMachine(t, testEscalationConfig(), escRepo)
	sess := nectarSession(false)
	sess.SetContext("escalation_count", "3")
	raw, err := sm.EncodeRejection(sm.PersistedRejection{Reason: "documento inválido", Attempt: 1})
	if err != nil {
		t.Fatal(err)
	}
	sess.SetContext(sm.CtxAgentInputRejection, raw)

	res := escalateOnce(t, m, sess)
	if res.NextState != sm.StateEscalated || escRepo.calls != 1 {
		t.Fatalf("la re-escalación por rechazo no cuenta para el tope: %s / %d", res.NextState, escRepo.calls)
	}
}

// P1-5(d): en Néctar el corte anti-bucle del `__resume__` le dice al paciente por qué vuelve al
// menú (allá no hubo aviso de no-show antes); el menú va después.
func TestEscalate_NectarResumeSuppressionTellsThePatient(t *testing.T) {
	m, _ := nectarMachine(t, testEscalationConfig(), nil)
	sess := nectarSession(false)
	msg := nectarMsg()
	msg.Text = "__resume__"

	res, err := m.Process(context.Background(), sess, msg)
	if err != nil {
		t.Fatal(err)
	}
	if res.NextState != sm.StateMainMenu {
		t.Fatalf("el corte deja la sesión en el menú, fue %s", res.NextState)
	}
	if len(res.Messages) != 2 {
		t.Fatalf("quiero el aviso y el menú, fueron %d mensajes: %+v", len(res.Messages), res.Messages)
	}
	if txt, ok := res.Messages[0].(*sm.TextMessage); !ok || txt.Text != resumeSuppressedNectarText {
		t.Errorf("primer mensaje = %+v, quiero el aviso %q", res.Messages[0], resumeSuppressedNectarText)
	}
	if _, ok := res.Messages[1].(*sm.ListMessage); !ok {
		t.Errorf("segundo mensaje = %+v, quiero el menú principal", res.Messages[1])
	}
}

// En Bird el corte del `__resume__` sigue mandando SOLO el menú: el aviso «No pudimos
// conectarte…» ya salió en el no-show.
func TestEscalate_BirdResumeSuppressionUnchanged(t *testing.T) {
	srv := escalationTestServer(t)
	m := sm.NewMachine()
	RegisterEscalationHandlers(m, bird.NewClientForTest(srv.URL), testEscalationConfig(), nil)
	sess := testSess(sm.StateEscalateToAgent)
	sess.ConversationID = "conv-test"

	res, err := m.Process(context.Background(), sess, textM("__resume__"))
	if err != nil {
		t.Fatal(err)
	}
	if res.NextState != sm.StateMainMenu {
		t.Fatalf("esperaba MAIN_MENU, fue %s", res.NextState)
	}
	if len(res.Messages) != 1 {
		t.Fatalf("Bird: solo el menú, fueron %d mensajes: %+v", len(res.Messages), res.Messages)
	}
	list, ok := res.Messages[0].(*sm.ListMessage)
	if !ok || list.Body != buildMainMenuList().Body {
		t.Errorf("Bird: el mensaje es el menú principal de hoy, fue %+v", res.Messages[0])
	}
}

// El sobre de la escalación lleva el candado que declara el paso donde se atascó el paciente.
func TestEscalate_EnvelopeCarriesResolveLock(t *testing.T) {
	returnMenuMachine()
	cfg := withProtocolV1(testEscalationConfig())
	m, nb := nectarMachine(t, cfg, nil)
	sess := nectarSession(true)
	sess.SetContext(sm.CtxPreAutoState, sm.StateUploadMedicalOrder)

	escalateOnce(t, m, sess)

	if len(nb.escalations) != 1 {
		t.Fatalf("se esperaba UNA escalación, hubo %d", len(nb.escalations))
	}
	if got := nb.escalations[0].ResolveLock; got != "retorno_pendiente" {
		t.Errorf("ResolveLock = %q, quiero retorno_pendiente", got)
	}
}

// P2-2: ANTES de agrupar la sesión no tiene cups_code, solo la orden leída: el equipo sugerido
// sale del primer CUPS de ocr_cups_json (sobre y evento usan el mismo código).
func TestEscalationEnvelope_TeamFromOCRBeforeGrouping(t *testing.T) {
	cfg := testEscalationConfig()
	cfg.NectarTeamByGroup = map[string]int64{"imagenes": 7}
	m, nb := nectarMachine(t, cfg, nil)
	sess := nectarSession(true)
	sess.SetContext("ocr_cups_json", `[{"cups_code":"883101","cups_name":"RM","quantity":1}]`)

	sink := &observability.CaptureSink{}
	stop := observability.StartCapture(sink)
	escalateOnce(t, m, sess)
	stop()

	if len(nb.escalations) != 1 || nb.escalations[0].SuggestedTeamID == nil || *nb.escalations[0].SuggestedTeamID != 7 {
		t.Fatalf("el equipo sugerido tiene que salir de la orden leída: %+v", nb.escalations)
	}
	if ev := sink.FindStep("escalacion", "equipo_sugerido"); ev == nil || ev.Attrs["grupo"] != "imagenes" {
		t.Fatalf("el evento usa el mismo código que el sobre: %+v", ev)
	}
}

// El cups_code de la sesión manda sobre la orden leída.
func TestTeamCupsCode_PrefersSessionCode(t *testing.T) {
	sess := nectarSession(false)
	sess.SetContext("ocr_cups_json", `[{"cups_code":"883101","quantity":1}]`)
	if got := teamCupsCode(sess); got != "883101" {
		t.Fatalf("sin cups_code sale de la orden: %q", got)
	}
	sess.SetContext("cups_code", "890274")
	if got := teamCupsCode(sess); got != "890274" {
		t.Fatalf("con cups_code manda el de la sesión: %q", got)
	}
	sess.SetContext("cups_code", "")
	sess.SetContext("ocr_cups_json", "no-es-json")
	if got := teamCupsCode(sess); got != "" {
		t.Fatalf("JSON inválido no inventa código: %q", got)
	}
}

// P2-6: la re-escalación por un dato del agente que no sirvió no la desvía el gate de
// disponibilidad (la causó una persona; desviarla dejaría al paciente sin asesor).
func TestEscalate_RejectedReescalationSkipsGate(t *testing.T) {
	m, nb := nectarMachine(t, testEscalationConfig(), nil)
	nb.unavailable = true
	sess := nectarSession(false)
	raw, err := sm.EncodeRejection(sm.PersistedRejection{Reason: "documento inválido", Attempt: 1})
	if err != nil {
		t.Fatal(err)
	}
	sess.SetContext(sm.CtxAgentInputRejection, raw)

	res := escalateOnce(t, m, sess)
	if res.NextState != sm.StateEscalated || len(nb.escalations) != 1 {
		t.Fatalf("la re-escalación por rechazo escala igual: %s / %d", res.NextState, len(nb.escalations))
	}

	// Control: sin rechazo, el mismo escenario sí se desvía.
	m2, nb2 := nectarMachine(t, testEscalationConfig(), nil)
	nb2.unavailable = true
	res2 := escalateOnce(t, m2, nectarSession(false))
	if res2.NextState != sm.StateMainMenu || len(nb2.escalations) != 0 {
		t.Fatalf("sin rechazo el gate desvía: %s / %d", res2.NextState, len(nb2.escalations))
	}
}

// P2-7: con el escalado apagado para la entidad no se reintenta ni se promete un asesor.
func TestEscalate_DisabledDoesNotPromiseAnAgent(t *testing.T) {
	m, nb := nectarMachine(t, testEscalationConfig(), nil)
	nb.escalateErr = nectar.ErrEscalationDisabled
	sess := nectarSession(true)

	sink := &observability.CaptureSink{}
	stop := observability.StartCapture(sink)
	res := escalateOnce(t, m, sess)
	stop()

	if len(nb.escalations) != 1 {
		t.Fatalf("un 403 permanente no se reintenta: %d intentos", len(nb.escalations))
	}
	if res.NextState != sm.StateFallbackMenu {
		t.Fatalf("estado: %s", res.NextState)
	}
	var text string
	for _, msg := range res.Messages {
		if b, ok := msg.(*sm.ButtonMessage); ok {
			text = b.Text
		}
	}
	if text != escalationDisabledText {
		t.Fatalf("texto: %q", text)
	}
	if sink.FindStep("escalacion", "escalation_disabled") == nil {
		t.Fatal("falta escalacion/escalation_disabled")
	}
}

// Cualquier otro fallo conserva el texto de siempre y se reintenta.
func TestEscalate_OtherFailureKeepsTheUsualText(t *testing.T) {
	m, nb := nectarMachine(t, testEscalationConfig(), nil)
	nb.escalateErr = io.ErrUnexpectedEOF
	res := escalateOnce(t, m, nectarSession(true))
	if len(nb.escalations) != escalateMaxRetries {
		t.Fatalf("intentos: %d", len(nb.escalations))
	}
	for _, msg := range res.Messages {
		if b, ok := msg.(*sm.ButtonMessage); ok && b.Text == escalationDisabledText {
			t.Fatal("un fallo transitorio no dice «apagado»")
		}
	}
}
