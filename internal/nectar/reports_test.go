package nectar

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"
)

// Las lecturas de informes (Plan 45/46) contra un falso Néctar que exige la firma sobre la URL
// completa y la clave de servicio.

type fakeReports struct {
	srv    *httptest.Server
	mu     sync.Mutex
	urls   []*url.URL
	status int
	body   string
}

func newFakeReports(t *testing.T, status int, body string) *fakeReports {
	f := &fakeReports{status: status, body: body}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		full := "http://" + r.Host + r.URL.String()
		if err := VerifyNotice(reqSecret, r.Header.Get(SignatureHeader), r.Header.Get(TimestampHeader), full, raw); err != nil {
			t.Errorf("firma rechazada: %v url=%s", err, full)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Header.Get(ServiceKeyHeader) != svcKey {
			t.Errorf("clave de servicio ausente o distinta: %q", r.Header.Get(ServiceKeyHeader))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.mu.Lock()
		f.urls = append(f.urls, r.URL)
		status, payload := f.status, f.body
		f.mu.Unlock()
		w.WriteHeader(status)
		_, _ = w.Write([]byte(payload))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeReports) client() *Client { return NewClient(f.srv.URL, svcKey, reqSecret) }

func (f *fakeReports) reply(status int, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status, f.body = status, body
}

const cyclesContractJSON = `{
  "cycles": [
    {
      "id": 812, "conversation_id": 4410, "previous_cycle_id": null, "team_id": 7,
      "agent_id": 31, "opened_at": "2026-11-02T13:04:11Z", "assigned_at": "2026-11-02T13:05:02Z",
      "first_response_at": "2026-11-02T13:06:40Z", "closed_at": "2026-11-02T13:20:00Z",
      "close_reason": "returned_to_bot", "backfilled": false,
      "contact_messages_before_first_response": 2
    },
    {
      "id": 813, "conversation_id": 4411, "previous_cycle_id": 812, "team_id": null,
      "agent_id": null, "opened_at": "2026-11-02T14:00:00Z", "assigned_at": null,
      "first_response_at": null, "closed_at": null, "close_reason": null, "backfilled": true,
      "contact_messages_before_first_response": 0
    }
  ],
  "next_cursor": null,
  "history_since": "2026-08-15T00:00:00Z"
}`

func TestGetWaitingCyclesSendsQuery(t *testing.T) {
	f := newFakeReports(t, http.StatusOK, `{"cycles":[],"next_cursor":null,"history_since":null}`)
	c := f.client()
	loc := time.FixedZone("x", -5*3600)
	since := time.Date(2026, 10, 30, 17, 18, 0, 0, loc) // 22:18Z
	until := since.Add(48 * time.Hour)

	if _, err := c.GetWaitingCycles(t.Context(), since, until, ""); err != nil {
		t.Fatalf("primera: %v", err)
	}
	if _, err := c.GetWaitingCycles(t.Context(), since, until, "abc=="); err != nil {
		t.Fatalf("segunda: %v", err)
	}
	if len(f.urls) != 2 {
		t.Fatalf("llamadas = %d", len(f.urls))
	}
	q1, q2 := f.urls[0].Query(), f.urls[1].Query()
	if f.urls[0].Path != WaitingCyclesPath {
		t.Errorf("ruta = %q", f.urls[0].Path)
	}
	if q1.Get("since") != "2026-10-30T22:18:00Z" || q1.Get("until") != "2026-11-01T22:18:00Z" {
		t.Errorf("since/until = %q / %q", q1.Get("since"), q1.Get("until"))
	}
	if q1.Get("limit") != "200" {
		t.Errorf("limit = %q", q1.Get("limit"))
	}
	if q1.Has("cursor") {
		t.Errorf("la primera página no lleva cursor: %q", q1.Get("cursor"))
	}
	if q2.Get("cursor") != "abc==" {
		t.Errorf("cursor = %q", q2.Get("cursor"))
	}
	if q1.Get("n") == "" || q1.Get("n") == q2.Get("n") {
		t.Errorf("n debe existir y cambiar: %q / %q", q1.Get("n"), q2.Get("n"))
	}
}

func TestGetWaitingCyclesDecodesContractShape(t *testing.T) {
	f := newFakeReports(t, http.StatusOK, cyclesContractJSON)
	page, err := f.client().GetWaitingCycles(t.Context(), time.Now().Add(-time.Hour), time.Now(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Cycles) != 2 {
		t.Fatalf("tramos = %d", len(page.Cycles))
	}
	a := page.Cycles[0]
	if a.ID != 812 || a.ConversationID != 4410 || a.PreviousCycleID != nil || a.TeamID == nil || *a.TeamID != 7 ||
		a.AgentID == nil || *a.AgentID != 31 || a.ContactMessagesBeforeFirstResponse != 2 || a.Backfilled {
		t.Errorf("tramo 1 mal leído: %+v", a)
	}
	if !a.OpenedAt.Equal(time.Date(2026, 11, 2, 13, 4, 11, 0, time.UTC)) {
		t.Errorf("opened_at = %v", a.OpenedAt)
	}
	if a.AssignedAt == nil || a.FirstResponseAt == nil || a.ClosedAt == nil || a.CloseReason == nil || *a.CloseReason != "returned_to_bot" {
		t.Errorf("tramo 1: fechas/causa ausentes: %+v", a)
	}
	b := page.Cycles[1]
	if b.PreviousCycleID == nil || *b.PreviousCycleID != 812 || !b.Backfilled {
		t.Errorf("tramo 2 mal leído: %+v", b)
	}
	if b.TeamID != nil || b.AgentID != nil || b.AssignedAt != nil || b.FirstResponseAt != nil ||
		b.ClosedAt != nil || b.CloseReason != nil {
		t.Errorf("los null deben quedar en nil: %+v", b)
	}
	if page.NextCursor != nil {
		t.Errorf("next_cursor null debe ser nil: %q", *page.NextCursor)
	}
	if page.HistorySince == nil || !page.HistorySince.Equal(time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("history_since = %v", page.HistorySince)
	}
}

func TestGetWaitingCyclesAccountKeyRequired(t *testing.T) {
	f := newFakeReports(t, http.StatusForbidden, `{"error":"account_key_required"}`)
	_, err := f.client().GetWaitingCycles(t.Context(), time.Now().Add(-time.Hour), time.Now(), "")
	if !errors.Is(err, ErrAccountKeyRequired) {
		t.Fatalf("err = %v", err)
	}
}

func TestGetWaitingCyclesBadQuery(t *testing.T) {
	f := newFakeReports(t, http.StatusBadRequest, `{"error":"invalid_query"}`)
	_, err := f.client().GetWaitingCycles(t.Context(), time.Now().Add(-time.Hour), time.Now(), "")
	if err == nil || errors.Is(err, ErrAccountKeyRequired) || errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v", err)
	}
	f.reply(http.StatusUnauthorized, `{"error":"x"}`)
	_, err = f.client().GetWaitingCycles(t.Context(), time.Now().Add(-time.Hour), time.Now(), "")
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("401: err = %v", err)
	}
}

func TestGetAgentsDecodes(t *testing.T) {
	f := newFakeReports(t, http.StatusOK, `{"agents":[
	  {"id":31,"name":"Laura M.","active":true,"presence":"available","teams":[{"id":7,"name":"Grupo A"}],"open_conversations":3},
	  {"id":32,"name":"Pedro","active":false,"presence":"offline","teams":[],"open_conversations":0}]}`)
	agents, err := f.client().GetAgents(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if f.urls[0].Path != AgentsPath || f.urls[0].Query().Get("n") == "" {
		t.Errorf("url = %v", f.urls[0])
	}
	if len(agents) != 2 {
		t.Fatalf("agentes = %d", len(agents))
	}
	a := agents[0]
	if a.ID != 31 || a.Name != "Laura M." || !a.Active || a.Presence != "available" || a.OpenConversations != 3 ||
		len(a.Teams) != 1 || a.Teams[0] != (ReportTeam{ID: 7, Name: "Grupo A"}) {
		t.Errorf("agente 1 mal leído: %+v", a)
	}
	if b := agents[1]; b.Active || b.Teams == nil || len(b.Teams) != 0 {
		t.Errorf("agente 2: teams [] debe quedar slice vacío: %+v", b)
	}

	f.reply(http.StatusForbidden, `{"error":"account_key_required"}`)
	if _, err := f.client().GetAgents(t.Context()); !errors.Is(err, ErrAccountKeyRequired) {
		t.Fatalf("403: err = %v", err)
	}
}
