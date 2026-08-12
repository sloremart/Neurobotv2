package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/config"
	"github.com/neuro-bot/neuro-bot/internal/session"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
)

func testEscalationConfig() *config.Config {
	return &config.Config{
		TeamRoutingEnabled: true,
		BirdTeamGrupoA:     "team-grupo-a",
		BirdTeamGrupoB:     "team-grupo-b",
		BirdTeamFallback:   "team-fallback",
		BirdAgentFallback:  "agent-fallback",
		TestingAlwaysOpen:  true, // el gate de horario de escalateHandler usa el reloj real; los tests no dependen de la hora de la corrida
	}
}

func TestEscalatedHandler_Noop(t *testing.T) {
	m := sm.NewMachine()
	m.Register(sm.StateEscalated, escalatedHandler())

	sess := testSess(sm.StateEscalated)
	sess.Status = session.StatusEscalated

	result, err := m.Process(context.Background(), sess, textM("any message"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateEscalated {
		t.Errorf("expected ESCALATED, got %s", result.NextState)
	}
	if len(result.Messages) != 0 {
		t.Errorf("expected no messages, got %d", len(result.Messages))
	}
}

func TestEscalateHandler_Success(t *testing.T) {
	// Create a test server that accepts escalation + messages + agents
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/workspaces/ws-test/agents":
			w.WriteHeader(200)
			w.Write([]byte(`{"results":[{"id":"agent-1","displayName":"Test Agent","teams":[{"id":"team-fallback","name":"CC"}],"availability":{"status":"active","activity":"available"},"rootItemAssignedCount":0}]}`))
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/search/feed-items"):
			w.WriteHeader(200)
			w.Write([]byte(`{"results":[{"id":"fi-conv-test","feedId":"channel:ch-test","closed":false}]}`))
		default:
			w.WriteHeader(200)
			w.Write([]byte(`{"id":"msg-ok"}`))
		}
	}))
	defer srv.Close()

	birdClient := bird.NewClientForTest(srv.URL)
	cfg := testEscalationConfig()

	m := sm.NewMachine()
	RegisterEscalationHandlers(m, birdClient, cfg, nil)

	sess := testSess(sm.StateEscalateToAgent)
	sess.Context["patient_name"] = "Juan"
	sess.ConversationID = "conv-test"

	msg := bird.InboundMessage{
		Phone:          "+573001234567",
		ConversationID: "conv-test",
	}
	result, err := m.Process(context.Background(), sess, msg)
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateEscalated {
		t.Errorf("expected ESCALATED, got %s", result.NextState)
	}
	if sess.Status != session.StatusEscalated {
		t.Errorf("expected session status escalated, got %s", sess.Status)
	}
}

// TestEscalateHandler_NoAgentsGate: si NO hay agentes disponibles, el bot NO escala (evita
// no-show/expiración); avisa el horario y vuelve al menú. Cubre todas las vías de escalación.
func TestEscalateHandler_NoAgentsGate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Endpoint de agentes → SIN agentes activos.
		if r.Method == "GET" && r.URL.Path == "/workspaces/ws-test/agents" {
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"results":[]}`))
			return
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"id":"ok"}`))
	}))
	defer srv.Close()

	m := sm.NewMachine()
	RegisterEscalationHandlers(m, bird.NewClientForTest(srv.URL), testEscalationConfig(), nil)

	sess := testSess(sm.StateEscalateToAgent)
	sess.ConversationID = "conv-test"
	result, err := m.Process(context.Background(), sess, bird.InboundMessage{Phone: "+573001234567", ConversationID: "conv-test"})
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState == sm.StateEscalated {
		t.Error("sin agentes NO debía escalar (ESCALATED)")
	}
	if result.NextState != sm.StateMainMenu {
		t.Errorf("esperaba volver a MAIN_MENU, got %s", result.NextState)
	}
	if len(result.Events) == 0 || result.Events[0].Type != "escalation_no_agents" {
		t.Errorf("esperaba evento escalation_no_agents, got %+v", result.Events)
	}
}

// mockEscalationCreator captura las llamadas a Create para asertar el registro por escalación.
type mockEscalationCreator struct {
	calls      int
	fromStates []string
	sessions   []string
}

func (m *mockEscalationCreator) Create(_ context.Context, sessionID, _, fromState, _, _, _ string) error {
	m.calls++
	m.fromStates = append(m.fromStates, fromState)
	m.sessions = append(m.sessions, sessionID)
	return nil
}

func TestEscalateHandler_RecordsEscalation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/workspaces/ws-test/agents":
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"results":[{"id":"agent-1","name":"Test Agent","teams":[{"id":"team-fallback","name":"CC"}],"availability":{"status":"active","activity":"available"},"rootItemAssignedCount":0}]}`))
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/search/feed-items"):
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"results":[{"id":"fi-conv-test","feedId":"channel:ch-test","closed":false}]}`))
		default:
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"id":"msg-ok"}`))
		}
	}))
	defer srv.Close()

	rec := &mockEscalationCreator{}
	m := sm.NewMachine()
	RegisterEscalationHandlers(m, bird.NewClientForTest(srv.URL), testEscalationConfig(), rec)

	sess := testSess(sm.StateEscalateToAgent)
	sess.Context["patient_name"] = "Juan"
	sess.Context["_pre_auto_state"] = "ASK_MANUAL_CUPS" // el paso donde se confundió
	sess.ConversationID = "conv-test"

	_, err := m.Process(context.Background(), sess, bird.InboundMessage{Phone: "+573001234567", ConversationID: "conv-test"})
	if err != nil {
		t.Fatal(err)
	}
	if rec.calls != 1 {
		t.Fatalf("expected 1 Create call, got %d", rec.calls)
	}
	if rec.fromStates[0] != "ASK_MANUAL_CUPS" {
		t.Errorf("expected from_state=ASK_MANUAL_CUPS, got %q", rec.fromStates[0])
	}
	if rec.sessions[0] != sess.ID {
		t.Errorf("expected session_id=%s, got %q", sess.ID, rec.sessions[0])
	}
}

func TestEscalateHandler_EmptyConversationFallback(t *testing.T) {
	// EscalateToAgent will fail with empty conversationID (hay agentes → pasa el gate de disponibilidad).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.URL.Path == "/workspaces/ws-test/agents" {
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"results":[{"id":"a1","teams":[{"id":"team-fallback"}],"availability":{"status":"active"}}]}`))
			return
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"id":"ok"}`))
	}))
	defer srv.Close()

	birdClient := bird.NewClientForTest(srv.URL)
	cfg := testEscalationConfig()
	m := sm.NewMachine()
	RegisterEscalationHandlers(m, birdClient, cfg, nil)

	sess := testSess(sm.StateEscalateToAgent)
	// No conversationID anywhere → EscalateToAgent("") returns error
	msg := bird.InboundMessage{Phone: "+573001234567"}
	result, err := m.Process(context.Background(), sess, msg)
	if err != nil {
		t.Fatal(err)
	}
	// Should fallback to FallbackMenu since escalation failed (restart/end)
	if result.NextState != sm.StateFallbackMenu {
		t.Errorf("expected FALLBACK_MENU on escalation failure, got %s", result.NextState)
	}

	// B (ciclo 98) — no abandonar al paciente: el mensaje debe reconocer que un asesor lo contactará,
	// NO el rebote frío anterior ("No pudimos conectarte..."). Se ofrecen las salidas restart/end.
	if len(result.Messages) == 0 {
		t.Fatal("expected a patient-facing message on escalation failure")
	}
	bm, ok := result.Messages[0].(*sm.ButtonMessage)
	if !ok {
		t.Fatalf("expected *ButtonMessage, got %T", result.Messages[0])
	}
	if !strings.Contains(bm.Text, "te contactará") {
		t.Errorf("expected non-abandonment copy (asesor te contactará), got %q", bm.Text)
	}
	if len(bm.Buttons) != 2 || bm.Buttons[0].Payload != "action:restart" || bm.Buttons[1].Payload != "action:end" {
		t.Errorf("expected restart/end buttons, got %+v", bm.Buttons)
	}

	// El terminal medible del funnel debe emitirse para no perder el residual.
	var hasEvent bool
	for _, e := range result.Events {
		if e.Type == "escalation_failed" {
			hasEvent = true
		}
	}
	if !hasEvent {
		t.Error("expected escalation_failed event to be emitted")
	}
}

func TestEscalateHandler_TeamRouting(t *testing.T) {
	var assignedPayloads []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assignedPayloads = append(assignedPayloads, r.URL.Path)
		switch {
		case r.Method == "GET" && r.URL.Path == "/workspaces/ws-test/agents":
			// Agent in Grupo A team
			w.WriteHeader(200)
			w.Write([]byte(`{"results":[{"id":"agent-a","displayName":"Agent A","teams":[{"id":"team-grupo-a","name":"Grupo A"}],"availability":{"status":"active","activity":"available"},"rootItemAssignedCount":0}]}`))
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/search/feed-items"):
			w.WriteHeader(200)
			w.Write([]byte(`{"results":[{"id":"fi-conv-test","feedId":"channel:ch-test","closed":false}]}`))
		default:
			w.WriteHeader(200)
			w.Write([]byte(`{"id":"msg-ok"}`))
		}
	}))
	defer srv.Close()

	birdClient := bird.NewClientForTest(srv.URL)
	cfg := testEscalationConfig()

	m := sm.NewMachine()
	RegisterEscalationHandlers(m, birdClient, cfg, nil)

	sess := testSess(sm.StateEscalateToAgent)
	sess.Context["cups_code"] = "883100" // Resonancia → Grupo A
	sess.ConversationID = "conv-test"

	msg := bird.InboundMessage{
		Phone:          "+573001234567",
		ConversationID: "conv-test",
	}
	result, err := m.Process(context.Background(), sess, msg)
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState != sm.StateEscalated {
		t.Errorf("expected ESCALATED, got %s", result.NextState)
	}
}

// ---------------------------------------------------------------------------
// Anti-bucle de escalación (incidente 11/12-ago-2026): un no-show devolvía la
// sesión a un estado AUTOMÁTICO determinista (VALIDATE_OCR→cups_none, bloqueo
// SANITAS…) que re-escalaba en el mismo Process, y el checker re-disparaba el
// no-show al minuto (ancla en last_patient_msg, que nunca avanza sin paciente).
// Resultado: 18 pacientes con 1 WhatsApp/min toda la noche (~10.800 envíos).
// ---------------------------------------------------------------------------

// escalationTestServer levanta un Bird fake CON agentes disponibles (el gate de
// disponibilidad pasa, como pasó en el incidente: presencia encendida de noche).
func escalationTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/workspaces/ws-test/agents":
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"results":[{"id":"agent-1","displayName":"Test Agent","teams":[{"id":"team-fallback","name":"CC"}],"availability":{"status":"active","activity":"available"},"rootItemAssignedCount":0}]}`))
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/search/feed-items"):
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"results":[{"id":"fi-conv-test","feedId":"channel:ch-test","closed":false}]}`))
		default:
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"id":"msg-ok"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestEscalateHandler_ResumeVirtualNeverReescalates: el __resume__ virtual (retorno de
// no-show o resume de agente) NUNCA debe re-escalar, aunque haya agentes disponibles.
// Es la vuelta 2 del bucle: sin este corte, VALIDATE_OCR re-evalúa el mismo contexto,
// vuelve a cups_none y re-escala → lazo cerrado con el checker de no-show.
func TestEscalateHandler_ResumeVirtualNeverReescalates(t *testing.T) {
	srv := escalationTestServer(t)

	m := sm.NewMachine()
	RegisterEscalationHandlers(m, bird.NewClientForTest(srv.URL), testEscalationConfig(), nil)

	sess := testSess(sm.StateEscalateToAgent)
	sess.ConversationID = "conv-test"

	result, err := m.Process(context.Background(), sess, textM("__resume__"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState == sm.StateEscalated {
		t.Error("un __resume__ virtual NO debe re-escalar (bucle no-show↔escalación)")
	}
	if sess.Status == session.StatusEscalated {
		t.Error("la sesión NO debe quedar escalada tras un __resume__")
	}
	if result.NextState != sm.StateMainMenu {
		t.Errorf("esperaba MAIN_MENU, got %s", result.NextState)
	}
	found := false
	for _, ev := range result.Events {
		if ev.Type == "escalation_suppressed_resume" {
			found = true
		}
	}
	if !found {
		t.Errorf("esperaba evento escalation_suppressed_resume, got %+v", result.Events)
	}
}

// TestEscalateHandler_SessionCap: tope duro de escalaciones por sesión. A la 4ª
// (contador ya en 3) se corta pase lo que pase — cortacircuito independiente del
// mecanismo que genere las escalaciones ("los flujos infinitos sin escape deben
// evitarse en todo lugar").
func TestEscalateHandler_SessionCap(t *testing.T) {
	srv := escalationTestServer(t)

	m := sm.NewMachine()
	RegisterEscalationHandlers(m, bird.NewClientForTest(srv.URL), testEscalationConfig(), nil)

	sess := testSess(sm.StateEscalateToAgent)
	sess.ConversationID = "conv-test"
	sess.Context["escalation_count"] = "3"

	result, err := m.Process(context.Background(), sess, textM("quiero un asesor"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState == sm.StateEscalated {
		t.Error("con el tope alcanzado NO debe escalar")
	}
	found := false
	for _, ev := range result.Events {
		if ev.Type == "escalation_cap_reached" {
			found = true
		}
	}
	if !found {
		t.Errorf("esperaba evento escalation_cap_reached, got %+v", result.Events)
	}
}

// TestEscalateHandler_CountsEscalations: cada escalación exitosa incrementa el contador
// de la sesión (alimenta el tope duro del test anterior).
func TestEscalateHandler_CountsEscalations(t *testing.T) {
	srv := escalationTestServer(t)

	m := sm.NewMachine()
	RegisterEscalationHandlers(m, bird.NewClientForTest(srv.URL), testEscalationConfig(), nil)

	sess := testSess(sm.StateEscalateToAgent)
	sess.ConversationID = "conv-test"

	if _, err := m.Process(context.Background(), sess, textM("asesor")); err != nil {
		t.Fatal(err)
	}
	if got := sess.GetContext("escalation_count"); got != "1" {
		t.Errorf("esperaba escalation_count=1 tras escalar, got %q", got)
	}
}

// TestEscalateHandler_OutOfHoursGate: con el centro CERRADO no se escala aunque Bird diga que
// hay agentes "disponibles" (presencia encendida de noche — condición del incidente 11/12-ago:
// el bucle escalaba a las 4 a.m. porque HasAvailableAgents devolvía true).
func TestEscalateHandler_OutOfHoursGate(t *testing.T) {
	srv := escalationTestServer(t) // agentes DISPONIBLES

	old := nowFunc
	nowFunc = func() time.Time { return time.Date(2026, 8, 12, 4, 0, 0, 0, time.UTC) } // miércoles 4 a.m.
	defer func() { nowFunc = old }()

	cfg := testEscalationConfig()
	cfg.TestingAlwaysOpen = false // activar el reloj real (stubbeado)

	m := sm.NewMachine()
	RegisterEscalationHandlers(m, bird.NewClientForTest(srv.URL), cfg, nil)

	sess := testSess(sm.StateEscalateToAgent)
	sess.ConversationID = "conv-test"

	result, err := m.Process(context.Background(), sess, textM("quiero un asesor"))
	if err != nil {
		t.Fatal(err)
	}
	if result.NextState == sm.StateEscalated {
		t.Error("con el centro cerrado NO debe escalar aunque Bird reporte agentes activos")
	}
	if result.NextState != sm.StateMainMenu {
		t.Errorf("esperaba MAIN_MENU, got %s", result.NextState)
	}
	found := false
	for _, ev := range result.Events {
		if ev.Type == "escalation_no_agents" && ev.Data["gate"] == "out_of_hours" {
			found = true
		}
	}
	if !found {
		t.Errorf("esperaba evento escalation_no_agents con gate=out_of_hours, got %+v", result.Events)
	}
}
