package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
	// EscalateToAgent will fail with empty conversationID
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte(`{"id":"ok"}`))
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
