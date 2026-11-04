package bird

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Los WARN de títulos truncados registran el largo, no el título: en las listas de citas el título
// lleva el procedimiento del paciente.
func TestTruncatedTitleWarns_LogLengthOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"m1"}`))
	}))
	defer srv.Close()
	logged := captureDebugLogs(t)
	c := NewClientForTest(srv.URL)

	_, _ = c.SendList("+573001234567", "conv-1", "Tus citas", "Ver", []ListSection{{
		Title: "Citas", Rows: []ListRow{{ID: "appt_77", Title: "RESONANCIA MAGNETICA CEREBRAL CONTRASTADA"}},
	}})
	_, _ = c.SendButtons("+573001234567", "conv-1", "¿Confirmas?", []Button{
		{Text: "ELECTROMIOGRAFIA MIEMBROS SUPERIORES", Payload: "confirm_77"},
	})

	out := logged.String()
	for _, leak := range []string{"RESONANCIA", "ELECTROMIOGRAFIA"} {
		if strings.Contains(out, leak) {
			t.Errorf("el WARN de truncado contiene %q: %s", leak, out)
		}
	}
	for _, keep := range []string{"wa_list_row_title_truncated", "wa_button_title_truncated", "original_len=", "appt_77", "confirm_77"} {
		if !strings.Contains(out, keep) {
			t.Errorf("el WARN conserva %q: %s", keep, out)
		}
	}
}
