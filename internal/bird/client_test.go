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
	err := c.EscalateToAgent(context.Background(), "", "", "team-1", "Team", "Patient", "fallback-team")
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
	err := c.EscalateToAgent(context.Background(), "conv-123", "+573001234567", "team-a", "Grupo A", "Edgar A.", "team-fallback")
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
	err := c.EscalateToAgent(context.Background(), "conv-1", "+573001234567", "team-a", "Grupo A", "Patient", "team-fallback")
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
	err := c.EscalateToAgent(context.Background(), "conv-1", "+573001234567", "team-a", "Grupo A", "Patient", "team-fallback")
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
	err := c.EscalateToAgent(context.Background(), "conv-1", "+573001234567", "team-a", "Grupo A", "Patient", "team-fallback")
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
	err := c.EscalateToAgent(context.Background(), "conv-1", "+573001234567", "team-a", "Grupo A", "Patient", "team-a")
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
	err := c.AssignFeedItem(context.Background(), "conv-123", "team-a", "agent-1")
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

func TestAssignFeedItem_EmptyConversation(t *testing.T) {
	c := NewClientForTest("http://localhost")
	err := c.AssignFeedItem(context.Background(), "", "team-a", "agent-1")
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
	err := c.AssignFeedItem(context.Background(), "conv-1", "team-a", "")
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
		if r.URL.Query().Get("status") != "active" {
			t.Error("expected status=active")
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
	err := c.EscalateToAgent(context.Background(), "", "+573001234567", "team-a", "Grupo A", "Patient", "team-fallback")
	if err != nil {
		t.Fatalf("expected no error (lookup by phone), got %v", err)
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
