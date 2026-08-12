package bird

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Acelera el sondeo de caché de conversation_id en los tests (en prod es 1s × 6 ≈ ventana para el webhook).
func init() { convCachePollInterval = time.Millisecond }

func TestSendText_PayloadAndResponse(t *testing.T) {
	var received map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &received)
		w.WriteHeader(200)
		w.Write([]byte(`{"id":"msg-123"}`))
	}))
	defer srv.Close()

	c := NewClientForTest(srv.URL)
	msgID, err := c.SendText("+573001234567", "", "Hola")
	if err != nil {
		t.Fatal(err)
	}
	if msgID != "msg-123" {
		t.Errorf("expected msg-123, got %s", msgID)
	}

	// Verify payload structure
	bodyMap := received["body"].(map[string]interface{})
	if bodyMap["type"] != "text" {
		t.Errorf("expected body.type=text, got %v", bodyMap["type"])
	}
}

func TestSendButtons_PayloadCorrect(t *testing.T) {
	var received map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &received)
		w.WriteHeader(200)
		w.Write([]byte(`{"id":"msg-btn"}`))
	}))
	defer srv.Close()

	c := NewClientForTest(srv.URL)
	btns := []Button{
		{Text: "Option 1", Payload: "opt1"},
		{Text: "Option 2", Payload: "opt2"},
	}
	msgID, err := c.SendButtons("+573001234567", "", "Choose:", btns)
	if err != nil {
		t.Fatal(err)
	}
	if msgID != "msg-btn" {
		t.Errorf("expected msg-btn, got %s", msgID)
	}

	// Without conversationID, buttons fall back to text format
	bodyMap := received["body"].(map[string]interface{})
	if bodyMap["type"] != "text" {
		t.Errorf("expected body.type=text (fallback), got %v", bodyMap["type"])
	}
	textMap := bodyMap["text"].(map[string]interface{})
	textStr := textMap["text"].(string)
	if !strings.Contains(textStr, "1. Option 1") || !strings.Contains(textStr, "2. Option 2") {
		t.Errorf("expected numbered text fallback, got %s", textStr)
	}
}

func TestSendList_PayloadCorrect(t *testing.T) {
	var received map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &received)
		w.WriteHeader(200)
		w.Write([]byte(`{"id":"msg-list"}`))
	}))
	defer srv.Close()

	c := NewClientForTest(srv.URL)
	sections := []ListSection{
		{Title: "Sec1", Rows: []ListRow{{ID: "r1", Title: "Row1"}}},
	}
	msgID, err := c.SendList("+573001234567", "", "body text", "View", sections)
	if err != nil {
		t.Fatal(err)
	}
	if msgID != "msg-list" {
		t.Errorf("expected msg-list, got %s", msgID)
	}

	// Without conversationID, list falls back to text format
	bodyMap := received["body"].(map[string]interface{})
	if bodyMap["type"] != "text" {
		t.Errorf("expected body.type=text (fallback), got %v", bodyMap["type"])
	}
	textMap := bodyMap["text"].(map[string]interface{})
	textStr := textMap["text"].(string)
	if !strings.Contains(textStr, "1. Row1") {
		t.Errorf("expected numbered text fallback, got %s", textStr)
	}
}

func TestSendTemplate_PayloadCorrect(t *testing.T) {
	var received map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &received)
		w.WriteHeader(200)
		w.Write([]byte(`{"id":"msg-tmpl"}`))
	}))
	defer srv.Close()

	c := NewClientForTest(srv.URL)
	c.channelIDTemplates = "ch-tmpl"
	tmpl := TemplateConfig{
		ProjectID: "proj-1",
		VersionID: "v1",
		Locale:    "es",
		Params:    []TemplateParam{{Type: "text", Key: "name", Value: "Juan"}},
	}
	msgID, err := c.SendTemplate("+573001234567", tmpl)
	if err != nil {
		t.Fatal(err)
	}
	if msgID != "msg-tmpl" {
		t.Errorf("expected msg-tmpl, got %s", msgID)
	}

	tmplField, ok := received["template"].(map[string]interface{})
	if !ok {
		t.Fatal("expected template field in payload")
	}
	params, ok := tmplField["parameters"].([]interface{})
	if !ok {
		t.Fatal("expected parameters to be an array")
	}
	if len(params) != 1 {
		t.Fatalf("expected 1 parameter, got %d", len(params))
	}
	firstParam, ok := params[0].(map[string]interface{})
	if !ok {
		t.Fatal("expected first parameter to be a map")
	}
	if firstParam["key"] != "name" || firstParam["value"] != "Juan" {
		t.Errorf("expected key=name, value=Juan, got %v", firstParam)
	}
}

func TestEscalateToAgent_EmptyConversationID(t *testing.T) {
	c := NewClientForTest("http://localhost")
	// No conversationID and no phone → cannot lookup → error
	_, _, err := c.EscalateToAgent(context.Background(), "", "", "team-1", "Team", "Patient", "fallback-team")
	if err == nil {
		t.Error("expected error for empty conversation ID")
	}
}

func TestUpdateFeedItem_EmptyConversation(t *testing.T) {
	err := NewClientForTest("http://localhost").UpdateFeedItem("", "msg-1", true, "", "")
	if err != nil {
		t.Errorf("expected nil for empty conversation, got %v", err)
	}
}

func TestSendMessage_4xxError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		w.Write([]byte(`{"error":"bad request"}`))
	}))
	defer srv.Close()

	c := NewClientForTest(srv.URL)
	_, err := c.SendText("+573001234567", "", "test")
	if err == nil {
		t.Error("expected error for 400 response")
	}
}

func TestSendMessage_5xxRetries(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(500)
	}))
	defer srv.Close()

	c := NewClientForTest(srv.URL)
	_, err := c.SendText("+573001234567", "", "test")
	if err == nil {
		t.Error("expected error after retries")
	}
	// sendMessage uses maxRetries=2 → 3 attempts total
	if attempts != 3 {
		t.Errorf("expected 3 attempts (1+2 retries), got %d", attempts)
	}
}

func TestSendMessage_AuthHeader(t *testing.T) {
	var authHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader = r.Header.Get("Authorization")
		w.WriteHeader(200)
		w.Write([]byte(`{"id":"ok"}`))
	}))
	defer srv.Close()

	c := NewClientForTest(srv.URL)
	c.apiKeyWA = "test-key-123"
	c.SendText("+573001234567", "", "test")
	if authHeader != "AccessKey test-key-123" {
		t.Errorf("expected 'AccessKey test-key-123', got %q", authHeader)
	}
}

// TestFetchMessageConversationID valida la vía SÍNCRONA de resolver el conversationId de un mensaje
// recién enviado (fix de "empty conversation ID": el GET del mensaje trae conversationId).
func TestFetchMessageConversationID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("expected GET, got %s", r.Method)
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"id":"msg-1","conversationId":"conv-xyz","body":{"type":"text"}}`))
	}))
	defer srv.Close()

	c := NewClientForTest(srv.URL)
	c.apiKeyWA = "test-key"
	if got := c.FetchMessageConversationID(context.Background(), "msg-1"); got != "conv-xyz" {
		t.Errorf("esperaba conv-xyz, got %q", got)
	}
	// message id vacío → "" sin llamar a la API.
	if got := c.FetchMessageConversationID(context.Background(), ""); got != "" {
		t.Errorf("esperaba \"\" para message id vacío, got %q", got)
	}
}

// TestFetchMessageConversationID_FromContextID valida el fix de la causa raíz del "empty conversation
// ID" crónico: la Channels API de Bird NO devuelve conversationId top-level; lo entrega en context.id
// con el formato "{conversationId}:{messageId}:{contactId}". El fetch debe extraer el primer segmento.
// Verificado contra Bird real (canal prod 61a8f3cc: el GET del mensaje trae context.id, no conversationId).
func TestFetchMessageConversationID_FromContextID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"id":"de35d664-msg","channelId":"61a8f3cc","context":{"type":"conversation_message","id":"cf9f624e-conv:de35d664-msg:66695e6b-contact"}}`))
	}))
	defer srv.Close()

	c := NewClientForTest(srv.URL)
	c.apiKeyWA = "test-key"
	if got := c.FetchMessageConversationID(context.Background(), "de35d664-msg"); got != "cf9f624e-conv" {
		t.Errorf("esperaba el conversationId del primer segmento de context.id, got %q", got)
	}
}

// TestCreateConversationForPhone_409ReusesExisting valida el fix del residual "empty conversation ID":
// ante 409 ContactAlreadyInConversation, Bird entrega el conversationId existente en details y el bot
// lo reutiliza como éxito (no falla → no cae a PICKUP MANUAL).
func TestCreateConversationForPhone_409ReusesExisting(t *testing.T) {
	// 409 con details.conversationId → se reutiliza.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(409)
		_, _ = w.Write([]byte(`{"code":"ContactAlreadyInConversation","message":"...","details":{"conversationId":"conv-existente"}}`))
	}))
	defer srv.Close()
	c := NewClientForTest(srv.URL)
	c.accessKeyID = "test-key"
	got, err := c.CreateConversationForPhone(context.Background(), "+573001234567")
	if err != nil {
		t.Fatalf("409 con conversationId no debía fallar: %v", err)
	}
	if got != "conv-existente" {
		t.Errorf("esperaba conv-existente del 409, got %q", got)
	}

	// 201 normal → devuelve el id top-level.
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"id":"conv-nueva"}`))
	}))
	defer srv2.Close()
	c2 := NewClientForTest(srv2.URL)
	c2.accessKeyID = "test-key"
	if got, err := c2.CreateConversationForPhone(context.Background(), "+573001234567"); err != nil || got != "conv-nueva" {
		t.Errorf("201 debía devolver conv-nueva, got %q err=%v", got, err)
	}

	// 409 SIN conversationId → sigue siendo error (no se inventa nada).
	srv3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(409)
		_, _ = w.Write([]byte(`{"code":"OtroConflicto","message":"..."}`))
	}))
	defer srv3.Close()
	c3 := NewClientForTest(srv3.URL)
	c3.accessKeyID = "test-key"
	if _, err := c3.CreateConversationForPhone(context.Background(), "+573001234567"); err == nil {
		t.Error("409 sin conversationId debía seguir siendo error")
	}
}

// TestFetchMessageConversationID_RetriesOn404 valida la instrumentación del residual "empty
// conversation ID" (auditoría 2026-07-23): un 4xx transitorio (p.ej. mensaje aún no materializado)
// se REINTENTA los fetchMsgConvTries intentos y termina en "" (el WARN final lleva status+body).
func TestFetchMessageConversationID_RetriesOn404(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"code":"NotFound","message":"message not found"}`))
	}))
	defer srv.Close()

	c := NewClientForTest(srv.URL)
	c.apiKeyWA = "test-key"
	if got := c.FetchMessageConversationID(context.Background(), "msg-404"); got != "" {
		t.Errorf("esperaba \"\" en 404 persistente, got %q", got)
	}
	if calls != fetchMsgConvTries {
		t.Errorf("esperaba %d intentos en 404 (consistencia eventual), got %d", fetchMsgConvTries, calls)
	}
}

// TestFetchMessageConversationID_AuthErrorNoRetry: 401/403 es permanente (key sin permiso de
// lectura) → corta al primer intento en vez de quemar la ventana completa.
func TestFetchMessageConversationID_AuthErrorNoRetry(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"code":"Unauthorized"}`))
	}))
	defer srv.Close()

	c := NewClientForTest(srv.URL)
	c.apiKeyWA = "test-key"
	if got := c.FetchMessageConversationID(context.Background(), "msg-401"); got != "" {
		t.Errorf("esperaba \"\" en 401, got %q", got)
	}
	if calls != 1 {
		t.Errorf("esperaba 1 solo intento en 401 (permanente), got %d", calls)
	}
}

// TestCreateConversationForPhone valida la capa 4d del handoff (auditoría 2026-07-23): cuando fetch y
// lookup fallan, se crea la conversación explícitamente vía Conversations API (accessKeyID) y se
// cachea el ID para las capas siguientes.
func TestCreateConversationForPhone(t *testing.T) {
	var gotPath, gotAuth string
	var gotPayload map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotPayload)
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"id":"conv-created-1"}`))
	}))
	defer srv.Close()

	c := NewClientForTest(srv.URL)
	c.accessKeyID = "conv-key"
	id, err := c.CreateConversationForPhone(context.Background(), "+573001234567")
	if err != nil {
		t.Fatalf("no esperaba error: %v", err)
	}
	if id != "conv-created-1" {
		t.Errorf("esperaba conv-created-1, got %q", id)
	}
	if gotPath != "/workspaces/ws-test/conversations" {
		t.Errorf("path inesperado: %s", gotPath)
	}
	if gotAuth != "AccessKey conv-key" {
		t.Errorf("esperaba accessKeyID (Conversations API), got %q", gotAuth)
	}
	if gotPayload["channelId"] != "ch-test" {
		t.Errorf("payload sin channelId del canal: %v", gotPayload)
	}
	parts, _ := gotPayload["participants"].([]interface{})
	if len(parts) != 1 {
		t.Fatalf("esperaba 1 participante, got %v", gotPayload["participants"])
	}
	p, _ := parts[0].(map[string]interface{})
	if p["identifierKey"] != "phonenumber" || p["identifierValue"] != "+573001234567" {
		t.Errorf("participante inesperado: %v", p)
	}
	if cached := c.GetCachedConversationID("+573001234567"); cached != "conv-created-1" {
		t.Errorf("esperaba el ID creado en cache, got %q", cached)
	}
}

// TestCreateConversationForPhone_Conflict: si Bird responde 409 (ResourceAlreadyExists) u otro 4xx,
// se devuelve error con status+body (enmascarado) para diagnóstico y NO se cachea nada.
func TestCreateConversationForPhone_Conflict(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(409)
		_, _ = w.Write([]byte(`{"code":"ResourceAlreadyExists","message":"another active conversation exists for contact +573001234567"}`))
	}))
	defer srv.Close()

	c := NewClientForTest(srv.URL)
	c.accessKeyID = "conv-key"
	id, err := c.CreateConversationForPhone(context.Background(), "+573001234567")
	if err == nil || id != "" {
		t.Fatalf("esperaba error en 409, got id=%q err=%v", id, err)
	}
	if !strings.Contains(err.Error(), "status 409") {
		t.Errorf("el error debe incluir el status: %v", err)
	}
	if strings.Contains(err.Error(), "573001234567") {
		t.Errorf("el body del error debe enmascarar el teléfono: %v", err)
	}
	if cached := c.GetCachedConversationID("+573001234567"); cached != "" {
		t.Errorf("no debe cachear en error, got %q", cached)
	}
}

func TestMessagesURL_UsesApiURL(t *testing.T) {
	c := &Client{apiURL: "https://custom.api.com", workspaceID: "ws1", channelID: "ch1"}
	url := c.messagesURL()
	expected := "https://custom.api.com/workspaces/ws1/channels/ch1/messages"
	if url != expected {
		t.Errorf("expected %s, got %s", expected, url)
	}
}

func TestTemplatesURL_FallsBackToChannelID(t *testing.T) {
	c := &Client{apiURL: "https://api.example.com", workspaceID: "ws1", channelID: "ch1"}
	url := c.templatesURL()
	if url != "https://api.example.com/workspaces/ws1/channels/ch1/messages" {
		t.Errorf("expected channelID fallback, got %s", url)
	}

	c.channelIDTemplates = "ch-tmpl"
	url2 := c.templatesURL()
	if url2 != "https://api.example.com/workspaces/ws1/channels/ch-tmpl/messages" {
		t.Errorf("expected channelIDTemplates, got %s", url2)
	}
}

// agentsJSON returns a JSON response for the agents API with the given agents.
func agentsJSON(agents ...AgentInfo) string {
	resp := AgentListResponse{Results: agents}
	b, _ := json.Marshal(resp)
	return string(b)
}

// feedItemSearchJSON returns a search/feed-items response for a conversation.
// Uses "fi-{convID}" as the feed item ID and "channel:ch-test" as the feed ID.
func feedItemSearchJSON(convID string) string {
	return `{"results":[{"id":"fi-` + convID + `","feedId":"channel:ch-test","closed":false}]}`
}

// isFeedItemSearch returns true if the request is a POST to search/feed-items.
func isFeedItemSearch(r *http.Request) bool {
	return r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/search/feed-items")
}

func TestEscalateToAgent_AssignsLeastLoaded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "PATCH" && r.URL.Path == "/workspaces/ws-test/conversations/conv-123":
			// MarkConversationEscalated
			w.WriteHeader(200)
		case r.Method == "GET" && r.URL.Path == "/workspaces/ws-test/agents":
			// ListActiveAgents — two agents in team-a, one has lower workload
			w.WriteHeader(200)
			w.Write([]byte(agentsJSON(
				AgentInfo{
					ID: "agent-busy", DisplayName: "Busy",
					Teams:                 []AgentTeam{{ID: "team-a", Name: "A"}},
					Availability:          AgentAvailability{Status: "active", Activity: "available"},
					RootItemAssignedCount: 5,
				},
				AgentInfo{
					ID: "agent-free", DisplayName: "Free",
					Teams:                 []AgentTeam{{ID: "team-a", Name: "A"}},
					Availability:          AgentAvailability{Status: "active", Activity: "available"},
					RootItemAssignedCount: 1,
				},
			)))
		case isFeedItemSearch(r):
			w.WriteHeader(200)
			w.Write([]byte(feedItemSearchJSON("conv-123")))
		case r.Method == "PATCH" && r.URL.Path == "/workspaces/ws-test/feeds/channel:ch-test/items/fi-conv-123":
			// AssignFeedItem — verify payload
			body, _ := io.ReadAll(r.Body)
			var payload map[string]interface{}
			json.Unmarshal(body, &payload)
			if payload["agentId"] != "agent-free" {
				t.Errorf("expected agent-free, got %v", payload["agentId"])
			}
			if payload["teamId"] != "team-a" {
				t.Errorf("expected team-a, got %v", payload["teamId"])
			}
			w.WriteHeader(200)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	c := NewClientForTest(srv.URL)
	_, _, err := c.EscalateToAgent(context.Background(), "conv-123", "+573001234567", "team-a", "Grupo A", "Edgar A.", "team-fallback")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestEscalateToAgent_FallbackTeam(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "PATCH" && r.URL.Path == "/workspaces/ws-test/conversations/conv-1":
			w.WriteHeader(200)
		case r.Method == "GET" && r.URL.Path == "/workspaces/ws-test/agents":
			// Agent only in fallback team, not in target team
			w.WriteHeader(200)
			w.Write([]byte(agentsJSON(AgentInfo{
				ID: "agent-fb", DisplayName: "Fallback Agent",
				Teams:                 []AgentTeam{{ID: "team-fallback", Name: "CC"}},
				Availability:          AgentAvailability{Status: "active", Activity: "available"},
				RootItemAssignedCount: 0,
			})))
		case isFeedItemSearch(r):
			w.WriteHeader(200)
			w.Write([]byte(feedItemSearchJSON("conv-1")))
		case r.Method == "PATCH" && r.URL.Path == "/workspaces/ws-test/feeds/channel:ch-test/items/fi-conv-1":
			body, _ := io.ReadAll(r.Body)
			var payload map[string]interface{}
			json.Unmarshal(body, &payload)
			if payload["teamId"] != "team-fallback" {
				t.Errorf("expected fallback team, got %v", payload["teamId"])
			}
			if payload["agentId"] != "agent-fb" {
				t.Errorf("expected agent-fb, got %v", payload["agentId"])
			}
			w.WriteHeader(200)
		default:
			w.WriteHeader(200)
		}
	}))
	defer srv.Close()

	c := NewClientForTest(srv.URL)
	_, _, err := c.EscalateToAgent(context.Background(), "conv-1", "+573001234567", "team-a", "Grupo A", "Patient", "team-fallback")
	if err != nil {
		t.Fatalf("expected no error (fallback), got %v", err)
	}
}

func TestEscalateToAgent_NoActiveAgents(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "PATCH" && r.URL.Path == "/workspaces/ws-test/conversations/conv-1":
			w.WriteHeader(200)
		case r.Method == "GET" && r.URL.Path == "/workspaces/ws-test/agents":
			// No agents at all
			w.WriteHeader(200)
			w.Write([]byte(`{"results":[]}`))
		case isFeedItemSearch(r):
			w.WriteHeader(200)
			w.Write([]byte(feedItemSearchJSON("conv-1")))
		default:
			w.WriteHeader(200)
		}
	}))
	defer srv.Close()

	c := NewClientForTest(srv.URL)
	_, _, err := c.EscalateToAgent(context.Background(), "conv-1", "+573001234567", "team-a", "Grupo A", "Patient", "team-fallback")
	if err != nil {
		t.Errorf("expected nil error (assigns to team when no agents), got %v", err)
	}
}

func TestEscalateToAgent_AgentsAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "PATCH" && r.URL.Path == "/workspaces/ws-test/conversations/conv-1":
			w.WriteHeader(200)
		case r.Method == "GET" && r.URL.Path == "/workspaces/ws-test/agents":
			w.WriteHeader(500)
			w.Write([]byte(`internal error`))
		case isFeedItemSearch(r):
			w.WriteHeader(200)
			w.Write([]byte(feedItemSearchJSON("conv-1")))
		default:
			w.WriteHeader(200)
		}
	}))
	defer srv.Close()

	c := NewClientForTest(srv.URL)
	_, _, err := c.EscalateToAgent(context.Background(), "conv-1", "+573001234567", "team-a", "Grupo A", "Patient", "team-fallback")
	if err != nil {
		t.Errorf("expected nil error (falls back to team-only), got %v", err)
	}
}

func TestEscalateToAgent_AllBusy_AssignsToTeamOnly(t *testing.T) {
	var assignPayload map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "PATCH" && r.URL.Path == "/workspaces/ws-test/conversations/conv-1":
			w.WriteHeader(200)
		case r.Method == "GET" && r.URL.Path == "/workspaces/ws-test/agents":
			// Agents online but all busy
			w.WriteHeader(200)
			w.Write([]byte(agentsJSON(AgentInfo{
				ID: "agent-1", DisplayName: "Busy Agent",
				Teams:                 []AgentTeam{{ID: "team-a", Name: "A"}},
				Availability:          AgentAvailability{Status: "active", Activity: "busy"},
				RootItemAssignedCount: 3,
			})))
		case isFeedItemSearch(r):
			w.WriteHeader(200)
			w.Write([]byte(feedItemSearchJSON("conv-1")))
		case r.Method == "PATCH" && r.URL.Path == "/workspaces/ws-test/feeds/channel:ch-test/items/fi-conv-1":
			body, _ := io.ReadAll(r.Body)
			json.Unmarshal(body, &assignPayload)
			w.WriteHeader(200)
		default:
			w.WriteHeader(200)
		}
	}))
	defer srv.Close()

	c := NewClientForTest(srv.URL)
	_, _, err := c.EscalateToAgent(context.Background(), "conv-1", "+573001234567", "team-a", "Grupo A", "Patient", "team-a")
	if err != nil {
		t.Fatalf("expected no error (assign to team), got %v", err)
	}
	// Busy agents are used as fallback — agent should be assigned
	if assignPayload["teamId"] != "team-a" {
		t.Errorf("expected team-a, got %v", assignPayload["teamId"])
	}
	if assignPayload["agentId"] != "agent-1" {
		t.Errorf("expected agentId=agent-1 (busy fallback), got %v", assignPayload["agentId"])
	}
}

func TestUpdateFeedItem_Success(t *testing.T) {
	var method, path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		path = r.URL.Path
		w.WriteHeader(200)
	}))
	defer srv.Close()

	c := NewClientForTest(srv.URL)
	err := c.UpdateFeedItem("conv-1", "msg-1", true, "team-1", "agent-1")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if method != "PATCH" {
		t.Errorf("expected PATCH, got %s", method)
	}
	if path != "/workspaces/ws-test/conversations/conv-1/feed-items/msg-1" {
		t.Errorf("unexpected path: %s", path)
	}
}

func TestUpdateFeedItem_ServerError_NoReturn(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		w.Write([]byte(`not found`))
	}))
	defer srv.Close()

	c := NewClientForTest(srv.URL)
	// 4xx from UpdateFeedItem is logged but NOT returned as error
	err := c.UpdateFeedItem("conv-1", "msg-1", true, "", "")
	if err != nil {
		t.Errorf("expected nil error (4xx is logged), got %v", err)
	}
}

func TestPlaceCall_NoVoiceChannel(t *testing.T) {
	c := NewClientForTest("http://localhost")
	_, err := c.PlaceCall("+573001234567", nil)
	if err == nil {
		t.Error("expected error when voice channel not configured")
	}
}

func TestMessagesURL_EmptyApiURL_FallsBack(t *testing.T) {
	c := &Client{workspaceID: "ws1", channelID: "ch1"}
	url := c.messagesURL()
	expected := "https://api.bird.com/workspaces/ws1/channels/ch1/messages"
	if url != expected {
		t.Errorf("expected fallback URL %s, got %s", expected, url)
	}
}

func TestTemplatesURL_EmptyApiURL_FallsBack(t *testing.T) {
	c := &Client{workspaceID: "ws1", channelID: "ch1"}
	url := c.templatesURL()
	expected := "https://api.bird.com/workspaces/ws1/channels/ch1/messages"
	if url != expected {
		t.Errorf("expected fallback URL %s, got %s", expected, url)
	}
}

func TestListActiveAgents_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if r.URL.Path != "/workspaces/ws-test/agents" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("agentStatuses") != "active" {
			t.Error("expected agentStatuses=active query param")
		}
		w.WriteHeader(200)
		w.Write([]byte(agentsJSON(
			AgentInfo{ID: "a1", DisplayName: "Agent 1", RootItemAssignedCount: 2},
			AgentInfo{ID: "a2", DisplayName: "Agent 2", RootItemAssignedCount: 0},
		)))
	}))
	defer srv.Close()

	c := NewClientForTest(srv.URL)
	agents, err := c.ListActiveAgents()
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(agents) != 2 {
		t.Fatalf("expected 2 agents, got %d", len(agents))
	}
	if agents[0].ID != "a1" || agents[1].ID != "a2" {
		t.Errorf("unexpected agent IDs: %s, %s", agents[0].ID, agents[1].ID)
	}
}

func TestAssignFeedItem_Success(t *testing.T) {
	var patchPath string
	var payload map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case isFeedItemSearch(r):
			w.WriteHeader(200)
			w.Write([]byte(feedItemSearchJSON("conv-123")))
		case r.Method == "PATCH":
			patchPath = r.URL.Path
			body, _ := io.ReadAll(r.Body)
			json.Unmarshal(body, &payload)
			w.WriteHeader(200)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	c := NewClientForTest(srv.URL)
	err := c.AssignFeedItem(context.Background(), "conv-123", "", "team-a", "agent-1")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if patchPath != "/workspaces/ws-test/feeds/channel:ch-test/items/fi-conv-123" {
		t.Errorf("unexpected path: %s", patchPath)
	}
	if payload["teamId"] != "team-a" {
		t.Errorf("expected team-a, got %v", payload["teamId"])
	}
	if payload["agentId"] != "agent-1" {
		t.Errorf("expected agent-1, got %v", payload["agentId"])
	}
}

// TestAssignFeedItem_ReopensClosed (BUG-007 + ciclo 100): conversación REABIERTA cuyo feed item está
// CERRADO. Bird rechaza reabrir+asignar en el MISMO PATCH (422 "closed or archived"; validado contra
// Bird real 2026-07-07). Debe hacerse en DOS PATCH: 1) {closed:false} para reabrir, 2) {teamId,agentId}
// para asignar. El test verifica el orden y el contenido de ambos.
func TestAssignFeedItem_ReopensClosed(t *testing.T) {
	var patches []map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case isFeedItemSearch(r):
			w.WriteHeader(200)
			// feed item CERRADO (conversación reabierta)
			_, _ = w.Write([]byte(`{"results":[{"id":"fi-closed","feedId":"channel:ch-test","closed":true}]}`))
		case r.Method == "PATCH":
			body, _ := io.ReadAll(r.Body)
			var p map[string]interface{}
			_ = json.Unmarshal(body, &p)
			patches = append(patches, p)
			w.WriteHeader(200)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	c := NewClientForTest(srv.URL)
	if err := c.AssignFeedItem(context.Background(), "conv-reopen", "", "team-a", "agent-1"); err != nil {
		t.Fatalf("expected no error (debe reabrir + asignar), got %v", err)
	}
	if len(patches) != 2 {
		t.Fatalf("esperaba 2 PATCH (reabrir + asignar), got %d: %+v", len(patches), patches)
	}
	// 1er PATCH: reabrir SOLO (sin campos de asignación, que es lo que Bird rechaza).
	if patches[0]["closed"] != false {
		t.Errorf("1er PATCH debe reabrir (closed:false), got %v", patches[0]["closed"])
	}
	if _, ok := patches[0]["teamId"]; ok {
		t.Errorf("1er PATCH NO debe llevar teamId (Bird rechaza asignar item cerrado), got %+v", patches[0])
	}
	// 2do PATCH: asignar sobre el item ya abierto.
	if patches[1]["teamId"] != "team-a" {
		t.Errorf("2do PATCH esperaba team-a, got %v", patches[1]["teamId"])
	}
	if patches[1]["agentId"] != "agent-1" {
		t.Errorf("2do PATCH esperaba agent-1, got %v", patches[1]["agentId"])
	}
}

// TestAssignFeedItem_ReopenConflictRetries verifica el fix del 409: si el reopen {closed:false}
// devuelve 409 (el estado del feed item cambió por concurrencia entre el search y el PATCH), el
// handoff NO se aborta — se reintenta el ciclo con un searchFeedItem fresco y en el 2º intento el
// reopen (ya sin conflicto) + la asignación completan. Antes, un 409 en el reopen abortaba y el
// paciente quedaba sin agente (auditoría ciclo 106: 5 casos reopen 409).
func TestAssignFeedItem_ReopenConflictRetries(t *testing.T) {
	var reopenCount, assignCount int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case isFeedItemSearch(r):
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"results":[{"id":"fi-closed","feedId":"channel:ch-test","closed":true}]}`))
		case r.Method == "PATCH":
			body, _ := io.ReadAll(r.Body)
			var p map[string]interface{}
			_ = json.Unmarshal(body, &p)
			if _, isAssign := p["teamId"]; isAssign {
				assignCount++
				w.WriteHeader(200)
				return
			}
			// reopen (closed:false, sin teamId): 409 en el 1er intento, 200 en el 2º.
			reopenCount++
			if reopenCount == 1 {
				w.WriteHeader(409)
				_, _ = w.Write([]byte(`{"code":"Conflict","message":"the item state changed"}`))
				return
			}
			w.WriteHeader(200)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	c := NewClientForTest(srv.URL)
	if err := c.AssignFeedItem(context.Background(), "conv-conflict", "", "team-a", "agent-1"); err != nil {
		t.Fatalf("esperaba éxito tras reintentar el 409, got %v", err)
	}
	if reopenCount != 2 {
		t.Errorf("esperaba 2 reopen (409 luego 200), got %d", reopenCount)
	}
	if assignCount != 1 {
		t.Errorf("esperaba 1 asignación tras el reopen exitoso, got %d", assignCount)
	}
}

func TestAssignFeedItem_EmptyConversation(t *testing.T) {
	c := NewClientForTest("http://localhost")
	err := c.AssignFeedItem(context.Background(), "", "", "team-a", "agent-1")
	if err != nil {
		t.Errorf("expected nil for empty conversation, got %v", err)
	}
}

func TestAssignFeedItem_NoAgent(t *testing.T) {
	var payload map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case isFeedItemSearch(r):
			w.WriteHeader(200)
			w.Write([]byte(feedItemSearchJSON("conv-1")))
		case r.Method == "PATCH":
			body, _ := io.ReadAll(r.Body)
			json.Unmarshal(body, &payload)
			w.WriteHeader(200)
		default:
			w.WriteHeader(200)
		}
	}))
	defer srv.Close()

	c := NewClientForTest(srv.URL)
	err := c.AssignFeedItem(context.Background(), "conv-1", "", "team-a", "")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if _, hasAgent := payload["agentId"]; hasAgent {
		t.Error("expected no agentId when empty")
	}
}

func TestLookupConversationByPhone_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if r.URL.Path != "/workspaces/ws-test/conversations" {
			t.Errorf("expected /workspaces/ws-test/conversations, got %s", r.URL.Path)
		}
		if r.URL.Query().Get("channelId") != "ch-test" {
			t.Error("expected channelId=ch-test")
		}
		// BUG-006: ya NO se filtra por status=active (incluir conversaciones reabiertas/cerradas).
		if r.URL.Query().Get("status") != "" {
			t.Errorf("no debe enviarse filtro status, got %q", r.URL.Query().Get("status"))
		}
		w.WriteHeader(200)
		w.Write([]byte(`{"results":[{"id":"conv-found-123","featuredParticipants":[{"contact":{"identifierValue":"+573001234567"}}]}]}`))
	}))
	defer srv.Close()

	c := NewClientForTest(srv.URL)
	convID, err := c.LookupConversationByPhone("+573001234567")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if convID != "conv-found-123" {
		t.Errorf("expected conv-found-123, got %s", convID)
	}
	// Should also be cached
	if cached := c.GetCachedConversationID("+573001234567"); cached != "conv-found-123" {
		t.Errorf("expected cached conv-found-123, got %s", cached)
	}
}

func TestLookupConversationByPhone_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		// Conversation exists but with a different phone
		w.Write([]byte(`{"results":[{"id":"conv-other","featuredParticipants":[{"contact":{"identifierValue":"+573009999999"}}]}]}`))
	}))
	defer srv.Close()

	c := NewClientForTest(srv.URL)
	convID, err := c.LookupConversationByPhone("+573001234567")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if convID != "" {
		t.Errorf("expected empty, got %s", convID)
	}
}

// TestLookupConversationByPhone_Pagination verifica que el lookup sigue el nextPageToken de la RAÍZ
// de la respuesta de Bird (no un objeto "pagination"). La conversación buscada solo aparece en la
// página 2 → si la paginación está rota, el lookup falla (era la causa de "empty conversation ID").
func TestLookupConversationByPhone_Pagination(t *testing.T) {
	var pagesServed int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		if r.URL.Query().Get("pageToken") == "" {
			// Página 1: otra conversación + nextPageToken en la RAÍZ
			pagesServed++
			_, _ = w.Write([]byte(`{"results":[{"id":"conv-other","featuredParticipants":[{"contact":{"identifierValue":"+573009999999"}}]}],"nextPageToken":"PAGE2"}`))
			return
		}
		// Página 2: la conversación del paciente, sin más token
		pagesServed++
		_, _ = w.Write([]byte(`{"results":[{"id":"conv-on-page-2","featuredParticipants":[{"contact":{"identifierValue":"+573001234567"}}]}]}`))
	}))
	defer srv.Close()

	c := NewClientForTest(srv.URL)
	convID, err := c.LookupConversationByPhone("+573001234567")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if convID != "conv-on-page-2" {
		t.Errorf("expected conv-on-page-2 (via pagination), got %q", convID)
	}
	if pagesServed != 2 {
		t.Errorf("expected 2 pages fetched, got %d", pagesServed)
	}
}

// TestLookupConversationByPhone_PhoneFormatTolerant verifica el fix de "empty conversation ID":
// Bird devuelve el identifierValue SIN '+' ("573001234567"); el lookup debe matchear igual contra
// "+573001234567" por la comparación tolerante (utils.SamePhone), no por igualdad exacta de string.
func TestLookupConversationByPhone_PhoneFormatTolerant(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"results":[{"id":"conv-noplus","featuredParticipants":[{"identifierValue":"573001234567"}]}]}`))
	}))
	defer srv.Close()

	c := NewClientForTest(srv.URL)
	convID, err := c.LookupConversationByPhone("+573001234567")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if convID != "conv-noplus" {
		t.Errorf("expected conv-noplus (match tolerante sin '+'), got %q", convID)
	}
}

func TestLookupConversationByPhone_EmptyPhone(t *testing.T) {
	c := NewClientForTest("http://localhost")
	convID, err := c.LookupConversationByPhone("")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if convID != "" {
		t.Errorf("expected empty, got %s", convID)
	}
}

func TestEscalateToAgent_LookupByPhone(t *testing.T) {
	// conversationID is empty but phone is provided → should lookup and succeed
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/workspaces/ws-test/conversations" && r.URL.Query().Get("channelId") != "":
			// LookupConversationByPhone — list active conversations
			w.WriteHeader(200)
			w.Write([]byte(`{"results":[{"id":"conv-looked-up","featuredParticipants":[{"contact":{"identifierValue":"+573001234567"}}]}]}`))
		case r.Method == "PATCH" && r.URL.Path == "/workspaces/ws-test/conversations/conv-looked-up":
			// MarkConversationEscalated
			w.WriteHeader(200)
		case r.Method == "GET" && r.URL.Path == "/workspaces/ws-test/agents":
			w.WriteHeader(200)
			w.Write([]byte(agentsJSON(AgentInfo{
				ID: "agent-1", DisplayName: "Agent",
				Teams:                 []AgentTeam{{ID: "team-a", Name: "A"}},
				Availability:          AgentAvailability{Status: "active", Activity: "available"},
				RootItemAssignedCount: 0,
			})))
		case isFeedItemSearch(r):
			w.WriteHeader(200)
			w.Write([]byte(feedItemSearchJSON("conv-looked-up")))
		case r.Method == "PATCH" && r.URL.Path == "/workspaces/ws-test/feeds/channel:ch-test/items/fi-conv-looked-up":
			// AssignFeedItem with the looked-up conversationID
			w.WriteHeader(200)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	c := NewClientForTest(srv.URL)
	// Empty conversationID, but phone provided → lookup succeeds
	_, _, err := c.EscalateToAgent(context.Background(), "", "+573001234567", "team-a", "Grupo A", "Patient", "team-fallback")
	if err != nil {
		t.Fatalf("expected no error (lookup by phone), got %v", err)
	}
}

// TestEscalateToAgent_ResolvesFromCache (fix conv_id): si el conversation_id está en CACHÉ (poblado por
// el webhook outbound del mensaje que la escalación envió), el handoff lo usa SIN recurrir al lookup
// global por lista (poco fiable). Verifica que el lookup NO se invoca.
func TestEscalateToAgent_ResolvesFromCache(t *testing.T) {
	lookupHit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/workspaces/ws-test/conversations":
			lookupHit = true
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"results":[]}`))
		case r.Method == "PATCH" && r.URL.Path == "/workspaces/ws-test/conversations/conv-cached":
			w.WriteHeader(200)
		case r.Method == "GET" && r.URL.Path == "/workspaces/ws-test/agents":
			w.WriteHeader(200)
			_, _ = w.Write([]byte(agentsJSON(AgentInfo{
				ID: "agent-1", DisplayName: "Agent",
				Teams:                 []AgentTeam{{ID: "team-a", Name: "A"}},
				Availability:          AgentAvailability{Status: "active", Activity: "available"},
				RootItemAssignedCount: 0,
			})))
		case isFeedItemSearch(r):
			w.WriteHeader(200)
			_, _ = w.Write([]byte(feedItemSearchJSON("conv-cached")))
		case r.Method == "PATCH" && r.URL.Path == "/workspaces/ws-test/feeds/channel:ch-test/items/fi-conv-cached":
			w.WriteHeader(200)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	c := NewClientForTest(srv.URL)
	c.CacheConversationID("+573001234567", "conv-cached") // simula el webhook outbound de Bird
	_, _, err := c.EscalateToAgent(context.Background(), "", "+573001234567", "team-a", "Grupo A", "Patient", "team-fallback")
	if err != nil {
		t.Fatalf("expected no error (resuelto por caché), got %v", err)
	}
	if lookupHit {
		t.Error("no debió pegarle al lookup global: el conversation_id estaba en caché")
	}
}

func TestPickLeastLoadedAgent(t *testing.T) {
	agents := []AgentInfo{
		{ID: "a1", Teams: []AgentTeam{{ID: "team-a"}}, Availability: AgentAvailability{Activity: "available"}, RootItemAssignedCount: 5},
		{ID: "a2", Teams: []AgentTeam{{ID: "team-a"}}, Availability: AgentAvailability{Activity: "available"}, RootItemAssignedCount: 2},
		{ID: "a3", Teams: []AgentTeam{{ID: "team-b"}}, Availability: AgentAvailability{Activity: "available"}, RootItemAssignedCount: 0},
		{ID: "a4", Teams: []AgentTeam{{ID: "team-a"}}, Availability: AgentAvailability{Activity: "busy"}, RootItemAssignedCount: 0},
	}

	// Should pick a2 (in team-a, available, lowest load)
	best := pickLeastLoadedAgent(agents, "team-a")
	if best == nil || best.ID != "a2" {
		t.Errorf("expected a2, got %v", best)
	}

	// Should pick a3 (only one in team-b)
	best = pickLeastLoadedAgent(agents, "team-b")
	if best == nil || best.ID != "a3" {
		t.Errorf("expected a3, got %v", best)
	}

	// No one in team-c
	best = pickLeastLoadedAgent(agents, "team-c")
	if best != nil {
		t.Errorf("expected nil for unknown team, got %v", best)
	}
}

func TestSendMessage_429Retries(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(429)
			return
		}
		w.WriteHeader(200)
		w.Write([]byte(`{"id":"msg-after-429"}`))
	}))
	defer srv.Close()

	c := NewClientForTest(srv.URL)
	msgID, err := c.SendText("+573001234567", "", "test")
	if err != nil {
		t.Fatalf("expected success after 429 retry, got %v", err)
	}
	if msgID != "msg-after-429" {
		t.Errorf("expected msg-after-429, got %s", msgID)
	}
	if attempts != 3 {
		t.Errorf("expected 3 attempts (2x429 + 1x200), got %d", attempts)
	}
}

func TestSendMessage_429ExhaustedRetries(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(429)
	}))
	defer srv.Close()

	c := NewClientForTest(srv.URL)
	_, err := c.SendText("+573001234567", "", "test")
	if err == nil {
		t.Error("expected error after exhausting retries on 429")
	}
	// sendMessage uses maxRetries=2 → 3 attempts total
	if attempts != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts)
	}
}

func TestParseRetryAfter(t *testing.T) {
	tests := []struct {
		input    string
		expected time.Duration
	}{
		{"", 2 * time.Second},
		{"5", 5 * time.Second},
		{"0", 2 * time.Second},
		{"-1", 2 * time.Second},
		{"999", 2 * time.Second},
		{"120", 120 * time.Second},
		{"abc", 2 * time.Second},
	}
	for _, tt := range tests {
		got := parseRetryAfter(tt.input)
		if got != tt.expected {
			t.Errorf("parseRetryAfter(%q) = %v, want %v", tt.input, got, tt.expected)
		}
	}
}

// TestCreateConversation_UsernameIdentifierKey (migración 040): para un contacto
// whatsappusername (identificador no-E.164) la creación de conversación debe declarar
// identifierKey=whatsappusername — la misma con la que Bird lo entrega en el webhook.
// Declararlo "phonenumber" garantizaba el fallo del último recurso del handoff.
func TestCreateConversation_UsernameIdentifierKey(t *testing.T) {
	var gotPayload map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotPayload)
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"id":"conv-user-1"}`))
	}))
	defer srv.Close()

	c := NewClientForTest(srv.URL)
	username := "CO.a1b2c3d4e5f6g7h8i9j0k1l2m3n4"
	id, err := c.CreateConversationForPhone(context.Background(), username)
	if err != nil {
		t.Fatalf("no esperaba error: %v", err)
	}
	if id != "conv-user-1" {
		t.Errorf("esperaba conv-user-1, got %q", id)
	}
	parts, _ := gotPayload["participants"].([]interface{})
	if len(parts) != 1 {
		t.Fatalf("esperaba 1 participante, got %v", gotPayload["participants"])
	}
	p, _ := parts[0].(map[string]interface{})
	if p["identifierKey"] != "whatsappusername" || p["identifierValue"] != username {
		t.Errorf("participante inesperado para username: %v", p)
	}
}

// TestSendList_UsernameSinConversacion_ResuelveYEntrega (H141-1, caso real lucy***bosa):
// un contacto whatsappusername SIN conversationID conocido debe RESOLVER su conversación
// (crear → 409 devuelve la existente, el caso normal: acaba de escribir) y entregar por
// ella — antes caía directo al gate E.164 y el paciente no recibía NADA.
func TestSendList_UsernameSinConversacion_ResuelveYEntrega(t *testing.T) {
	username := "lucy.bosa"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/workspaces/ws-test/conversations":
			w.WriteHeader(409)
			_, _ = w.Write([]byte(`{"code":"ContactAlreadyInConversation","details":{"conversationId":"conv-lucy"}}`))
		case r.Method == "POST" && r.URL.Path == "/workspaces/ws-test/conversations/conv-lucy/messages":
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{"id":"msg-list-lucy"}`))
		default:
			t.Errorf("request inesperado: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer srv.Close()

	c := NewClientForTest(srv.URL)
	id, err := c.SendList(username, "", "Elige una opción", "Ver opciones",
		[]ListSection{{Title: "Menú", Rows: []ListRow{{ID: "op1", Title: "Opción 1"}}}})
	if err != nil {
		t.Fatalf("el username debe entregarse vía conversación resuelta, got err: %v", err)
	}
	if id != "msg-list-lucy" {
		t.Errorf("esperaba msg-list-lucy, got %q", id)
	}
	if cached := c.GetCachedConversationID(username); cached != "conv-lucy" {
		t.Errorf("la conversación resuelta debe quedar cacheada, got %q", cached)
	}
}

// TestSendText_UsernameSinConversacion_ResuelveYEntrega: mismo comportamiento para texto.
func TestSendText_UsernameSinConversacion_ResuelveYEntrega(t *testing.T) {
	username := "CO.a1b2c3d4e5f6g7h8i9j0k1l2m3n4"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/workspaces/ws-test/conversations":
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{"id":"conv-nueva"}`))
		case r.Method == "POST" && r.URL.Path == "/workspaces/ws-test/conversations/conv-nueva/messages":
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{"id":"msg-text-1"}`))
		default:
			t.Errorf("request inesperado: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer srv.Close()

	c := NewClientForTest(srv.URL)
	id, err := c.SendText(username, "", "Hola")
	if err != nil {
		t.Fatalf("no esperaba error: %v", err)
	}
	if id != "msg-text-1" {
		t.Errorf("esperaba msg-text-1, got %q", id)
	}
}
