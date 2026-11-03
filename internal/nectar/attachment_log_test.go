package nectar

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// M-6: el descarte de una url ajena dice QUÉ anfitrión se rechazó, y solo eso: ni la ruta ni la
// consulta (pueden llevar firmas o datos).
func TestInbound_DiscardedURLLogsOnlyTheHost(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(prev)

	attachmentNotice("image", `{"url":"https://evil.example.com/ruta/x.jpg?sig=secreta"}`).
		ToInboundMessage(attachmentBase)

	out := buf.String()
	if !strings.Contains(out, "host=evil.example.com") {
		t.Fatalf("falta el host en el log: %s", out)
	}
	if strings.Contains(out, "sig=secreta") || strings.Contains(out, "/ruta/") {
		t.Fatalf("el log no lleva la URL completa: %s", out)
	}
}
