package handlers

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/session"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
)

// El aviso "te voy a conectar con un agente" viaja en el RESULT del estado que escala (o del
// helper/interceptor que lo dispara). El handler de escalación NO debe hacer un envío directo
// adicional al paciente cuando ya hay conversationID: eso duplicaba ~700 mensajes/semana.
// Las notas internas (draft:true, solo Inbox del agente) sí se envían y no cuentan.
func TestEscalateHandler_NoDirectPatientText(t *testing.T) {
	var patientTexts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/workspaces/ws-test/agents":
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"results":[{"id":"agent-1","displayName":"Test Agent","teams":[{"id":"team-fallback","name":"CC"}],"availability":{"status":"active","activity":"available"},"rootItemAssignedCount":0}]}`))
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/search/feed-items"):
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"results":[{"id":"fi-conv-test","feedId":"channel:ch-test","closed":false}]}`))
		case r.Method == "POST" && strings.Contains(r.URL.Path, "/conversations/") && strings.HasSuffix(r.URL.Path, "/messages"):
			body, _ := io.ReadAll(r.Body)
			// draft:true = nota interna (Inbox), no llega al paciente; lo demás sí.
			if !strings.Contains(string(body), `"draft":true`) {
				patientTexts.Add(1)
			}
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"id":"msg-ok"}`))
		default:
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"id":"msg-ok"}`))
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
		t.Fatalf("expected ESCALATED, got %s", result.NextState)
	}
	if sess.Status != session.StatusEscalated {
		t.Fatalf("expected escalated status, got %s", sess.Status)
	}
	if got := patientTexts.Load(); got != 0 {
		t.Errorf("el handler no debe enviar texto directo al paciente (el aviso va en el result del caller): hubo %d envíos", got)
	}
}
