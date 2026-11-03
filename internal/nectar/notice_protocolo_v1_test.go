package nectar

import (
	"strings"
	"testing"
)

// §5.1 y §5.3 del diseño: lo que el LECTOR de avisos gana con v1. Las dos piezas son
// defensivas —ninguna cambia lo que el bot hace con un aviso normal— y las dos tapan un agujero
// que ya existía.

func TestNotice_ProtocolAusenteEsCero(t *testing.T) {
	n, err := ParseNotice([]byte(`{"event_id":"e","kind":"mensaje_entrante","conversation_id":7}`))
	if err != nil {
		t.Fatal(err)
	}
	if n.Protocol != 0 {
		t.Fatalf("ausente = el dialecto de hoy = 0, no %d", n.Protocol)
	}
	if n.Protocol > ProtocolMax {
		t.Fatal("y desde luego no dispara la alerta de versión desconocida")
	}
}

func TestNotice_ProtocolConocidoYDesconocido(t *testing.T) {
	for _, caso := range []struct {
		protocol int
		alerta   bool
	}{{1, false}, {2, true}, {99, true}} {
		body := `{"event_id":"e","kind":"mensaje_entrante","conversation_id":7,"protocol":` +
			strings.TrimSpace(itoa(caso.protocol)) + `}`
		n, err := ParseNotice([]byte(body))
		if err != nil {
			t.Fatalf("un protocolo desconocido NUNCA tumba el parseo: %v", err)
		}
		if (n.Protocol > ProtocolMax) != caso.alerta {
			t.Errorf("protocol=%d: alerta=%v", caso.protocol, n.Protocol > ProtocolMax)
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// (1) del §5.3: con `id` y sin `url`, la URL se compone de la raíz de Néctar. Attachment.ID se
// parseaba desde el primer día y no se usaba en NINGUNA parte.
func TestInbound_AdjuntoSinURLSeComponeDelID(t *testing.T) {
	body := `{"event_id":"e","kind":"mensaje_entrante","conversation_id":7,
	          "message":{"id":1,"kind":"image","attachment":{"id":"abc123"}}}`
	n, _ := ParseNotice([]byte(body))
	msg := n.ToInboundMessage("https://nectar.colibrixa.com/")
	if msg.MessageType != "image" || msg.ImageURL != "https://nectar.colibrixa.com/api/bot/attachments/abc123" {
		t.Fatalf("la URL se compone del id: %+v", msg)
	}
	// Y la `url` explícita MANDA cuando viene y es de Néctar; una de otro anfitrión no se sigue
	// (attachment_origin_test.go).
	body = `{"event_id":"e","kind":"mensaje_entrante","conversation_id":7,
	         "message":{"id":1,"kind":"document","attachment":{"id":"abc123","url":"https://nectar.colibrixa.com/api/bot/attachments/abc123?v=2"}}}`
	n, _ = ParseNotice([]byte(body))
	if msg := n.ToInboundMessage("https://nectar.colibrixa.com"); msg.DocumentURL != "https://nectar.colibrixa.com/api/bot/attachments/abc123?v=2" {
		t.Fatalf("%+v", msg)
	}
}

// (2) del §5.3: sin NADA con que descargarlo, el adjunto no se despacha como adjunto. El paso de
// la orden médica veía un mensaje de imagen SIN imagen y se quedaba mirando una URL vacía.
func TestInbound_AdjuntoSinURLUtilizableNoSeDespachaComoImagen(t *testing.T) {
	for _, kind := range []string{"image", "document"} {
		body := `{"event_id":"e","kind":"mensaje_entrante","conversation_id":7,
		          "message":{"id":1,"kind":"` + kind + `","attachment":{"id":""}}}`
		n, _ := ParseNotice([]byte(body))
		msg := n.ToInboundMessage("https://nectar.colibrixa.com")
		if msg.MessageType != UnusableAttachmentType {
			t.Errorf("%s sin url: tipo=%q (se esperaba %q)", kind, msg.MessageType, UnusableAttachmentType)
		}
		if msg.ImageURL != "" || msg.DocumentURL != "" {
			t.Errorf("%s sin url: no puede quedar una URL vacía haciéndose pasar por adjunto: %+v", kind, msg)
		}
	}
	// Y sin raíz configurada pasa lo mismo aunque haya id: no hay con qué componerla.
	body := `{"event_id":"e","kind":"mensaje_entrante","conversation_id":7,
	          "message":{"id":1,"kind":"image","attachment":{"id":"abc123"}}}`
	n, _ := ParseNotice([]byte(body))
	if msg := n.ToInboundMessage(""); msg.MessageType != UnusableAttachmentType {
		t.Fatalf("sin raíz de Néctar no hay URL que componer: %+v", msg)
	}
}
