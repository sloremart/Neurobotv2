package nectar

import "testing"

// La URL del adjunto solo puede apuntar al Néctar configurado (mismo esquema y anfitrión que
// NECTAR_BASE_URL): el contrato sirve los adjuntos en GET /api/bot/attachments/{id} de Néctar,
// nunca desde otro anfitrión. Lo demás se recompone desde el id, escapado como segmento de ruta.

const attachmentBase = "https://nectar.colibrixa.com"

func attachmentNotice(kind, attachment string) Notice {
	n, _ := ParseNotice([]byte(`{"event_id":"e","kind":"mensaje_entrante","conversation_id":7,
	   "message":{"id":1,"kind":"` + kind + `","attachment":` + attachment + `}}`))
	return n
}

func TestInbound_AttachmentURLStaysOnNectarOrigin(t *testing.T) {
	cases := map[string]struct {
		attachment string
		want       string
	}{
		"url de Néctar se usa tal cual": {
			`{"id":"abc123","url":"https://nectar.colibrixa.com/api/bot/attachments/abc123"}`,
			attachmentBase + "/api/bot/attachments/abc123",
		},
		"anfitrión en mayúsculas es el mismo": {
			`{"id":"abc123","url":"https://NECTAR.colibrixa.com/api/bot/attachments/abc123"}`,
			"https://NECTAR.colibrixa.com/api/bot/attachments/abc123",
		},
		"otro anfitrión se recompone desde el id": {
			`{"id":"abc123","url":"http://192.168.1.207/interno"}`,
			attachmentBase + "/api/bot/attachments/abc123",
		},
		"otro esquema se recompone desde el id": {
			`{"id":"abc123","url":"http://nectar.colibrixa.com/api/bot/attachments/abc123"}`,
			attachmentBase + "/api/bot/attachments/abc123",
		},
		"file:// se recompone desde el id": {
			`{"id":"abc123","url":"file:///etc/passwd"}`,
			attachmentBase + "/api/bot/attachments/abc123",
		},
		"el id se escapa como segmento": {
			`{"id":"../../x"}`,
			attachmentBase + "/api/bot/attachments/..%2F..%2Fx",
		},
		"otro anfitrión sin id no deja URL": {
			`{"url":"https://evil.example.com/x.jpg"}`,
			"",
		},
	}
	for name, tc := range cases {
		msg := attachmentNotice("image", tc.attachment).ToInboundMessage(attachmentBase)
		if msg.ImageURL != tc.want {
			t.Errorf("%s: ImageURL=%q, quiero %q", name, msg.ImageURL, tc.want)
		}
		if tc.want == "" && msg.MessageType != UnusableAttachmentType {
			t.Errorf("%s: sin URL utilizable el tipo es %q", name, msg.MessageType)
		}
	}
}

// Sin raíz de Néctar no hay contra qué comprobar la URL: no se descarga nada.
func TestInbound_AttachmentURLWithoutBaseIsUnusable(t *testing.T) {
	msg := attachmentNotice("document", `{"id":"abc123","url":"https://nectar.colibrixa.com/api/bot/attachments/abc123"}`).
		ToInboundMessage("")
	if msg.MessageType != UnusableAttachmentType || msg.DocumentURL != "" {
		t.Fatalf("%+v", msg)
	}
}

func TestAttachmentURLEscapesTheID(t *testing.T) {
	c := NewClient(attachmentBase+"/", svcKey, reqSecret)
	if got := c.AttachmentURL("a/b+c="); got != attachmentBase+"/api/bot/attachments/a%2Fb+c=" {
		t.Fatalf("AttachmentURL = %q", got)
	}
}
