package bird

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// C-21: con LOG_LEVEL=debug, los volcados de envíos y respuestas de Bird no dejan el teléfono, el
// nombre del paciente ni el cuerpo en claro (el ReplaceAttr global no conoce "to" ni "payload").
func TestDebugLogs_NoPatientData(t *testing.T) {
	const reply = `{"id":"m1","receiver":{"contacts":[{"identifierValue":"+573001234567","displayName":"María López"}]}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/conversations/") {
			_, _ = w.Write([]byte(`no-json displayName María López`))
			return
		}
		_, _ = w.Write([]byte(reply))
	}))
	defer srv.Close()
	c := NewClientForTest(srv.URL)

	var logged bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(previous)

	if _, err := c.SendTemplate("+573001234567", TemplateConfig{
		ProjectID: "p", Params: []TemplateParam{{Key: "patient_name", Value: "María López"}},
	}); err != nil {
		t.Fatalf("SendTemplate: %v", err)
	}
	_, _ = c.sendToConversation("conv-1", map[string]string{"type": "text"}, true)

	if !strings.Contains(logged.String(), "send_template_payload") {
		t.Fatalf("el volcado de Debug sigue existiendo: %s", logged.String())
	}
	for _, leak := range []string{"3001234567", "María", "López"} {
		if strings.Contains(logged.String(), leak) {
			t.Errorf("el log de Debug contiene %q: %s", leak, logged.String())
		}
	}
}
