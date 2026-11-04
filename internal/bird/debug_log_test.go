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

// captureDebugLogs redirige slog a un buffer en nivel Debug durante el test.
func captureDebugLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var logged bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &logged
}

// H3: la lista lleva datos de salud del paciente (procedimiento, médico, fecha); el Debug solo deja
// cuántas filas hay y sus ids.
func TestDebugLogs_ListKeepsOnlyRowIDs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"m1"}`))
	}))
	defer srv.Close()
	logged := captureDebugLogs(t)

	_, _ = NewClientForTest(srv.URL).SendList("+573001234567", "conv-1", "Tus citas con el Dr. Ortega", "Ver citas",
		[]ListSection{{Title: "Citas", Rows: []ListRow{{ID: "appt_77", Title: "RESONANCIA CEREBRO", Description: "12/11 8:00 am"}}}})

	out := logged.String()
	for _, leak := range []string{"RESONANCIA", "Ortega", "12/11"} {
		if strings.Contains(out, leak) {
			t.Errorf("el Debug de la lista contiene %q: %s", leak, out)
		}
	}
	if !strings.Contains(out, "appt_77") {
		t.Errorf("el Debug de la lista conserva los ids de las filas: %s", out)
	}
}

// H5: el Debug de la plantilla y de la respuesta conserva lo que sirve para depurar un rechazo
// (proyecto, idioma, claves de los parámetros, id del mensaje) sin los valores del paciente.
func TestDebugLogs_TemplateKeepsStructure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"msg-tmpl-9","conversationId":"conv-9","receiver":{"displayName":"María López"}}`))
	}))
	defer srv.Close()
	logged := captureDebugLogs(t)

	if _, err := NewClientForTest(srv.URL).SendTemplate("+573001234567", TemplateConfig{
		ProjectID: "proj-recordatorio", Locale: "es",
		Params: []TemplateParam{{Key: "patient_name", Value: "María López"}},
	}); err != nil {
		t.Fatalf("SendTemplate: %v", err)
	}
	out := logged.String()
	for _, want := range []string{"proj-recordatorio", "locale=es", "patient_name", "msg-tmpl-9", "conv-9"} {
		if !strings.Contains(out, want) {
			t.Errorf("el Debug conserva %q: %s", want, out)
		}
	}
	if strings.Contains(out, "María") {
		t.Errorf("el Debug no lleva los valores del paciente: %s", out)
	}
}

// Los WARN de errores de Bird conservan el cuerpo para diagnosticar, recortado (300) y sin
// secuencias largas de dígitos.
func TestWarnErrorBody_Trimmed(t *testing.T) {
	long := `{"code":"InvalidTag","message":"` + strings.Repeat("x", 600) + ` +573001234567 María López"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(long))
	}))
	defer srv.Close()
	logged := captureDebugLogs(t)

	_ = NewClientForTest(srv.URL).TagConversation("+573001234567", "conv-1", "tag")
	out := logged.String()
	if !strings.Contains(out, "InvalidTag") {
		t.Errorf("el WARN conserva el inicio del cuerpo: %s", out)
	}
	if strings.Contains(out, "María") || strings.Contains(out, "3001234567") || strings.Count(out, "x") > 320 {
		t.Errorf("el cuerpo del WARN se recorta a 300 caracteres: %s", out)
	}
}
