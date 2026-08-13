package notifications

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/config"
	"github.com/neuro-bot/neuro-bot/internal/services"
)

// ---------------------------------------------------------------------------
// H130-1/H142-2: ~60/día de escalation_no_conversation — la escalación del
// recordatorio día-antes nacía sin conversationId y se rendía sin intentar
// resolverlo, aunque pending.BirdMessageID (persistido) permite obtenerlo del
// propio template, y crear la conversación garantiza canal como último recurso.
// ---------------------------------------------------------------------------

// resolutionServer registra qué rutas se tocaron para asertar la escalera.
type resolutionServer struct {
	mu        sync.Mutex
	notePosts int
	assigns   int
	convGets  int
	creates   int
}

func (rs *resolutionServer) start(t *testing.T, msgHasConv bool, createStatus int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rs.mu.Lock()
		defer rs.mu.Unlock()
		switch {
		// GET del template enviado (FetchMessageConversationID)
		case r.Method == "GET" && strings.Contains(r.URL.Path, "/messages/msg-tmpl-1"):
			rs.convGets++
			w.WriteHeader(200)
			if msgHasConv {
				_, _ = w.Write([]byte(`{"id":"msg-tmpl-1","conversationId":"conv-resolved"}`))
			} else {
				_, _ = w.Write([]byte(`{"id":"msg-tmpl-1"}`))
			}
		// Crear conversación (último recurso)
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/conversations"):
			rs.creates++
			w.WriteHeader(createStatus)
			if createStatus == 201 {
				_, _ = w.Write([]byte(`{"id":"conv-created"}`))
			} else {
				_, _ = w.Write([]byte(`{"code":"boom"}`))
			}
		// Nota interna (draft) a la conversación resuelta/creada
		case r.Method == "POST" && strings.Contains(r.URL.Path, "/conversations/") && strings.HasSuffix(r.URL.Path, "/messages"):
			rs.notePosts++
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{"id":"note-1"}`))
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/agents"):
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"results":[{"id":"agent-1","teams":[{"id":"team-fallback"}],"availability":{"status":"active"},"rootItemAssignedCount":0}]}`))
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/search/feed-items"):
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"results":[{"id":"fi-1","feedId":"channel:ch-test","closed":false}]}`))
		case r.Method == "PATCH":
			rs.assigns++
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"id":"ok"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newEscalationTestManager(srvURL string) *NotificationManager {
	return NewNotificationManager(bird.NewClientForTest(srvURL),
		services.NewAppointmentService(&mockApptRepoNotif{}, nil),
		&config.Config{BirdTeamFallback: "team-fallback"})
}

// TestEscalateToAgent_ResuelveCanalDelPropioTemplate: sin convID pero con BirdMessageID,
// la escalación debe resolver la conversación del template (capa b) y completar nota+asignación.
func TestEscalateToAgent_ResuelveCanalDelPropioTemplate(t *testing.T) {
	rs := &resolutionServer{}
	srv := rs.start(t, true, 500)

	m := newEscalationTestManager(srv.URL)
	pending := &PendingNotification{
		Phone: "+573001234567", AppointmentID: "APT001", Type: "confirmation",
		BirdMessageID: "msg-tmpl-1", // persistido desde el envío del template
	}
	m.escalateToAgent(pending, escalationNoResponse)

	if pending.ConversationID != "conv-resolved" {
		t.Fatalf("debia resolver la conversacion del template, got %q", pending.ConversationID)
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.convGets == 0 {
		t.Error("debia consultar el mensaje del template (FetchMessageConversationID)")
	}
	if rs.notePosts == 0 {
		t.Error("con canal resuelto la NOTA al agente debe enviarse")
	}
	if rs.creates != 0 {
		t.Error("no debia llegar al ultimo recurso (crear conversacion)")
	}
}

// TestEscalateToAgent_CreaConversacionComoUltimoRecurso: si el template no resuelve,
// crear la conversación (capa d) y completar la escalación por ella.
func TestEscalateToAgent_CreaConversacionComoUltimoRecurso(t *testing.T) {
	rs := &resolutionServer{}
	srv := rs.start(t, false, 201)

	m := newEscalationTestManager(srv.URL)
	pending := &PendingNotification{
		Phone: "+573001234567", AppointmentID: "APT002", Type: "confirmation",
		BirdMessageID: "msg-tmpl-1",
	}
	m.escalateToAgent(pending, escalationNoResponse)

	if pending.ConversationID != "conv-created" {
		t.Fatalf("debia crear la conversacion como ultimo recurso, got %q", pending.ConversationID)
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.creates == 0 {
		t.Error("debia intentar crear la conversacion")
	}
	if rs.notePosts == 0 {
		t.Error("con canal creado la NOTA al agente debe enviarse")
	}
}

// TestEscalateToAgent_SinCanalPosible_NoNota: si TODA la escalera falla, se conserva el
// comportamiento previo (evento escalation_no_conversation, sin nota) — sin bucles ni panics.
func TestEscalateToAgent_SinCanalPosible_NoNota(t *testing.T) {
	rs := &resolutionServer{}
	srv := rs.start(t, false, 500)

	m := newEscalationTestManager(srv.URL)
	pending := &PendingNotification{
		Phone: "+573001234567", AppointmentID: "APT003", Type: "confirmation",
		BirdMessageID: "msg-tmpl-1",
	}
	m.escalateToAgent(pending, escalationNoResponse)

	if pending.ConversationID != "" {
		t.Fatalf("sin canal posible el convID debe quedar vacio, got %q", pending.ConversationID)
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.notePosts != 0 {
		t.Error("sin canal NO debe haber nota")
	}
}
