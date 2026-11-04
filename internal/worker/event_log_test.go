package worker

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/statemachine"
)

// H2: el log INFO de un evento no deja el teléfono, el correo ni el documento del paciente en
// claro; el evento (lo que se guarda en chat_events) queda intacto.
func TestLogResultEvents_RedactsPatientData(t *testing.T) {
	var logged bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logged, nil)))
	defer slog.SetDefault(previous)

	data := map[string]interface{}{"phone": "+573103343616", "email": "maria@example.com", "doc": "1000000689"}
	logResultEvents("sess-1", "+573103343616", []statemachine.Event{{Type: "contact_updated", Data: data}})

	out := logged.String()
	for _, leak := range []string{"3103343616", "maria@example.com", "1000000689"} {
		if strings.Contains(out, leak) {
			t.Errorf("el log del evento contiene %q: %s", leak, out)
		}
	}
	if !strings.Contains(out, "contact_updated") {
		t.Errorf("el log conserva el tipo de evento: %s", out)
	}
	if data["phone"] != "+573103343616" || data["doc"] != "1000000689" {
		t.Errorf("el evento que va a la BD no se toca: %v", data)
	}
}
