package bird

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// voiceCallBody imita la respuesta de Bird Voice: trae los teléfonos del paciente y de la clínica
// en claro, y la URL de la grabación.
const voiceCallBody = `{"id":"call-1","status":"completed","to":"+573001234567","from":"+576015550000",` +
	`"recordingUrl":"https://media.example/rec/call-1.wav",` +
	`"callCommands":[{"command":"gather","status":"completed","result":{"keys":"1"}}],` +
	`"command":"gather","result":{"keys":"1"}}`

// B-12: el cuerpo crudo de Bird Voice no se escribe en los logs INFO; basta con el status y el largo.
func TestVoiceResponses_LogsNoRawBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(voiceCallBody))
	}))
	defer srv.Close()
	c := &Client{httpClient: srv.Client(), conversationsAPIURL: srv.URL, workspaceID: "ws", voiceChannelID: "vc"}

	var logged bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, nil)))
	defer slog.SetDefault(previous)

	details, err := c.GetCallDetails("call-1")
	if err != nil || GatherKeysFromCallDetails(details) != "1" {
		t.Fatalf("la respuesta se sigue leyendo: keys=%v err=%v", details, err)
	}
	keys, err := c.GetGatherResult("call-1", "cmd-1")
	if err != nil || keys != "1" {
		t.Fatalf("el gather se sigue leyendo: keys=%q err=%v", keys, err)
	}

	out := logged.String()
	for _, leak := range []string{"3001234567", "6015550000", "recordingUrl", "call-1.wav"} {
		if strings.Contains(out, leak) {
			t.Errorf("el log INFO contiene %q del cuerpo crudo: %s", leak, out)
		}
	}
	if !strings.Contains(out, "body_len=") || !strings.Contains(out, "status=200") {
		t.Errorf("el log conserva status y largo del cuerpo: %s", out)
	}
}
