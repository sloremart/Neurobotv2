package bird

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strconv"
	"testing"
	"time"
)

// computeBirdSignature replica el algoritmo exacto de Bird (ejemplo oficial Go):
// 1. bh = SHA256(body) → [32]byte raw
// 2. payload = fmt.Sprintf("%s\n%s\n%s", timestamp, url, bh)  ← %s = raw bytes
// 3. sig = base64(HMAC-SHA256(key, payload))
func computeBirdSignature(secret, timestamp, url string, body []byte) string {
	bh := sha256.Sum256(body)
	var m bytes.Buffer
	fmt.Fprintf(&m, "%s\n%s\n%s", timestamp, url, bh)

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(m.Bytes())
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func TestVerifyWebhookSignature_Valid(t *testing.T) {
	c := &Client{WebhookSecret: "my-secret"}
	body := []byte(`{"event":"message.created"}`)
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	url := "https://example.com/api/webhooks/whatsapp"

	sig := computeBirdSignature("my-secret", timestamp, url, body)

	if !c.VerifyWebhookSignature(sig, timestamp, url, body) {
		t.Error("expected valid signature")
	}
}

func TestVerifyWebhookSignature_Invalid(t *testing.T) {
	c := &Client{WebhookSecret: "my-secret"}
	body := []byte(`{"event":"message.created"}`)
	if c.VerifyWebhookSignature("aW52YWxpZC1zaWduYXR1cmU=", "1700000000", "https://example.com/webhook", body) {
		t.Error("expected invalid signature to be rejected")
	}
}

// TestVerifyWebhookSignature_TimestampAge cubre la ventana anti-replay de 24h: los reintentos de Bird
// (que reenvían el timestamp original viejo) deben aceptarse mientras sean < 24h, y rechazarse después.
// Antes (15 min) se rechazaban los reintentos → 401 en bucle → mensajes de pacientes perdidos.
func TestVerifyWebhookSignature_TimestampAge(t *testing.T) {
	c := &Client{WebhookSecret: "my-secret"}
	body := []byte(`{"event":"whatsapp.inbound"}`)
	url := "https://example.com/api/webhooks/whatsapp"

	// 23h de antigüedad, firma válida → DENTRO de la ventana de 24h → debe aceptarse (con 15 min fallaba).
	old23h := strconv.FormatInt(time.Now().Add(-23*time.Hour).Unix(), 10)
	if !c.VerifyWebhookSignature(computeBirdSignature("my-secret", old23h, url, body), old23h, url, body) {
		t.Error("firma válida de 23h debería aceptarse (cubre los reintentos de Bird)")
	}

	// 25h de antigüedad, firma válida → FUERA de la ventana → rechazada por edad.
	old25h := strconv.FormatInt(time.Now().Add(-25*time.Hour).Unix(), 10)
	if c.VerifyWebhookSignature(computeBirdSignature("my-secret", old25h, url, body), old25h, url, body) {
		t.Error("firma de 25h debería rechazarse por edad")
	}

	// Timestamp futuro >24h → también rechazado (el chequeo usa el valor absoluto).
	future := strconv.FormatInt(time.Now().Add(25*time.Hour).Unix(), 10)
	if c.VerifyWebhookSignature(computeBirdSignature("my-secret", future, url, body), future, url, body) {
		t.Error("timestamp futuro >24h debería rechazarse")
	}
}

func TestVerifyWebhookSignature_Empty(t *testing.T) {
	c := &Client{WebhookSecret: "my-secret"}
	body := []byte(`test`)

	if c.VerifyWebhookSignature("", "1700000000", "https://example.com/webhook", body) {
		t.Error("empty signature should return false")
	}
	if c.VerifyWebhookSignature("something", "", "https://example.com/webhook", body) {
		t.Error("empty timestamp should return false")
	}
}

func TestVerifyWebhookSignature_InvalidBase64(t *testing.T) {
	c := &Client{WebhookSecret: "my-secret"}
	body := []byte(`test`)
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)

	if c.VerifyWebhookSignature("not-valid-base64!!!", timestamp, "https://example.com/webhook", body) {
		t.Error("invalid base64 should return false")
	}
}

func TestVerifyWebhookSignature_ExpiredTimestamp(t *testing.T) {
	c := &Client{WebhookSecret: "my-secret"}
	body := []byte(`test`)
	url := "https://example.com/webhook"
	// Más de 24h (maxTimestampAge) → fuera de la ventana anti-replay → rechazado por edad.
	oldTimestamp := strconv.FormatInt(time.Now().Add(-25*time.Hour).Unix(), 10)
	sig := computeBirdSignature("my-secret", oldTimestamp, url, body)

	if c.VerifyWebhookSignature(sig, oldTimestamp, url, body) {
		t.Error("expired timestamp should return false")
	}
}

func TestVerifyWebhookSignature_DifferentURL(t *testing.T) {
	c := &Client{WebhookSecret: "my-secret"}
	body := []byte(`{"event":"message.created"}`)
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)

	sig := computeBirdSignature("my-secret", timestamp, "https://example.com/api/webhooks/whatsapp", body)

	if c.VerifyWebhookSignature(sig, timestamp, "https://example.com/api/webhooks/other", body) {
		t.Error("different URL should make signature invalid")
	}
}

func TestParseInboundMessage_Text(t *testing.T) {
	event := WebhookEvent{
		Payload: WebhookPayload{
			ID: "msg-1",
			Sender: SenderInfo{
				DisplayName: "Juan",
				Contacts:    []Contact{{IdentifierValue: "+573001234567"}},
			},
			Body:           MessageBody{Type: "text", Text: TextBody{Text: "Hola"}},
			ConversationID: "conv-1",
		},
	}

	msg := ParseInboundMessage(event)
	if msg.ID != "msg-1" {
		t.Errorf("expected msg-1, got %s", msg.ID)
	}
	if msg.Phone != "+573001234567" {
		t.Errorf("expected phone, got %s", msg.Phone)
	}
	if msg.MessageType != "text" {
		t.Errorf("expected text, got %s", msg.MessageType)
	}
	if msg.Text != "Hola" {
		t.Errorf("expected Hola, got %s", msg.Text)
	}
	if msg.ConversationID != "conv-1" {
		t.Errorf("expected conv-1, got %s", msg.ConversationID)
	}
	if msg.IsPostback {
		t.Error("should not be postback")
	}
}

func TestParseInboundMessage_Postback(t *testing.T) {
	event := WebhookEvent{
		Payload: WebhookPayload{
			ID: "msg-pb",
			Sender: SenderInfo{
				Contacts: []Contact{{IdentifierValue: "+573001234567"}},
			},
			Body: MessageBody{
				Type: "text",
				Text: TextBody{
					Text: "button text",
					Actions: []Action{
						{Type: "postback", Postback: Postback{Text: "Confirmar", Payload: "confirm"}},
					},
				},
			},
		},
	}

	msg := ParseInboundMessage(event)
	if msg.MessageType != "postback" {
		t.Errorf("expected postback, got %s", msg.MessageType)
	}
	if !msg.IsPostback {
		t.Error("expected IsPostback=true")
	}
	if msg.PostbackPayload != "confirm" {
		t.Errorf("expected confirm, got %s", msg.PostbackPayload)
	}
}

func TestParseInboundMessage_InteractivePostback(t *testing.T) {
	event := WebhookEvent{
		Payload: WebhookPayload{
			ID: "msg-interactive",
			Sender: SenderInfo{
				Contacts: []Contact{{IdentifierValue: "+573001234567"}},
			},
			Body: MessageBody{
				Type: "interactive",
				Text: TextBody{
					Text: "Sí, registrarme",
					Actions: []Action{
						{Type: "postback", Postback: Postback{Text: "Sí, registrarme", Payload: "register_yes"}},
					},
				},
			},
		},
	}

	msg := ParseInboundMessage(event)
	if msg.MessageType != "postback" {
		t.Errorf("expected postback, got %s", msg.MessageType)
	}
	if !msg.IsPostback {
		t.Error("expected IsPostback=true for interactive")
	}
	if msg.PostbackPayload != "register_yes" {
		t.Errorf("expected register_yes, got %s", msg.PostbackPayload)
	}
}

func TestParseInboundMessage_ListPostback(t *testing.T) {
	event := WebhookEvent{
		Payload: WebhookPayload{
			ID: "msg-list",
			Sender: SenderInfo{
				Contacts: []Contact{{IdentifierValue: "+573001234567"}},
			},
			Body: MessageBody{
				Type: "list",
				Text: TextBody{
					Text: "Agendar cita",
					Actions: []Action{
						{Type: "postback", Postback: Postback{Text: "Agendar cita", Payload: "agendar"}},
					},
				},
			},
		},
	}

	msg := ParseInboundMessage(event)
	if msg.MessageType != "postback" {
		t.Errorf("expected postback, got %s", msg.MessageType)
	}
	if !msg.IsPostback {
		t.Error("expected IsPostback=true for list")
	}
	if msg.PostbackPayload != "agendar" {
		t.Errorf("expected agendar, got %s", msg.PostbackPayload)
	}
}

func TestParseInboundMessage_ListPostback_ListKey(t *testing.T) {
	// Bird may place the postback under the "list" JSON key instead of "text"
	event := WebhookEvent{
		Payload: WebhookPayload{
			ID: "msg-list-key",
			Sender: SenderInfo{
				Contacts: []Contact{{IdentifierValue: "+573001234567"}},
			},
			Body: MessageBody{
				Type: "list",
				List: TextBody{
					Text: "Agendar cita",
					Actions: []Action{
						{Type: "postback", Postback: Postback{Text: "Agendar cita", Payload: "agendar"}},
					},
				},
			},
		},
	}

	msg := ParseInboundMessage(event)
	if msg.MessageType != "postback" {
		t.Errorf("expected postback, got %s", msg.MessageType)
	}
	if !msg.IsPostback {
		t.Error("expected IsPostback=true for list under list key")
	}
	if msg.PostbackPayload != "agendar" {
		t.Errorf("expected agendar, got %s", msg.PostbackPayload)
	}
}

func TestParseInboundMessage_InteractivePostback_InteractiveKey(t *testing.T) {
	// Bird may place the postback under the "interactive" JSON key
	event := WebhookEvent{
		Payload: WebhookPayload{
			ID: "msg-interactive-key",
			Sender: SenderInfo{
				Contacts: []Contact{{IdentifierValue: "+573001234567"}},
			},
			Body: MessageBody{
				Type: "interactive",
				Interactive: TextBody{
					Text: "Confirmar",
					Actions: []Action{
						{Type: "postback", Postback: Postback{Text: "Confirmar", Payload: "reg_confirm"}},
					},
				},
			},
		},
	}

	msg := ParseInboundMessage(event)
	if msg.MessageType != "postback" {
		t.Errorf("expected postback, got %s", msg.MessageType)
	}
	if msg.PostbackPayload != "reg_confirm" {
		t.Errorf("expected reg_confirm, got %s", msg.PostbackPayload)
	}
}

func TestParseInboundMessage_Image(t *testing.T) {
	event := WebhookEvent{
		Payload: WebhookPayload{
			ID: "msg-img",
			Sender: SenderInfo{
				Contacts: []Contact{{IdentifierValue: "+573001234567"}},
			},
			Body: MessageBody{
				Type: "image",
				Text: TextBody{Text: "https://example.com/image.jpg"},
			},
		},
	}

	msg := ParseInboundMessage(event)
	if msg.MessageType != "image" {
		t.Errorf("expected image, got %s", msg.MessageType)
	}
	if msg.ImageURL != "https://example.com/image.jpg" {
		t.Errorf("expected image URL, got %s", msg.ImageURL)
	}
}

func TestParseInboundMessage_Audio(t *testing.T) {
	event := WebhookEvent{
		Payload: WebhookPayload{
			ID:     "msg-aud",
			Sender: SenderInfo{Contacts: []Contact{{IdentifierValue: "+573001234567"}}},
			Body:   MessageBody{Type: "audio"},
		},
	}
	msg := ParseInboundMessage(event)
	if msg.MessageType != "audio" {
		t.Errorf("expected audio, got %s", msg.MessageType)
	}
}

func TestParseInboundMessage_NoSenderContacts(t *testing.T) {
	event := WebhookEvent{
		Payload: WebhookPayload{
			ID:   "msg-nocontact",
			Body: MessageBody{Type: "text", Text: TextBody{Text: "hi"}},
		},
	}
	msg := ParseInboundMessage(event)
	if msg.Phone != "" {
		t.Errorf("expected empty phone, got %s", msg.Phone)
	}
}

func TestExtractPostbackPayload_NoActions(t *testing.T) {
	body := MessageBody{Type: "text", Text: TextBody{Text: "hello"}}
	payload, ok := ExtractPostbackPayload(body)
	if ok {
		t.Error("expected false for no actions")
	}
	if payload != "" {
		t.Errorf("expected empty payload, got %s", payload)
	}
}

func TestExtractPostbackPayload_NonPostbackAction(t *testing.T) {
	body := MessageBody{
		Type: "text",
		Text: TextBody{
			Actions: []Action{{Type: "reply", Postback: Postback{Payload: "data"}}},
		},
	}
	_, ok := ExtractPostbackPayload(body)
	if ok {
		t.Error("expected false for non-postback action type")
	}
}

func TestParseInboundMessage_Video(t *testing.T) {
	event := WebhookEvent{
		Payload: WebhookPayload{
			ID:     "msg-vid",
			Sender: SenderInfo{Contacts: []Contact{{IdentifierValue: "+573001234567"}}},
			Body:   MessageBody{Type: "video"},
		},
	}
	msg := ParseInboundMessage(event)
	if msg.MessageType != "video" {
		t.Errorf("expected video, got %s", msg.MessageType)
	}
}

func TestParseInboundMessage_Location(t *testing.T) {
	event := WebhookEvent{
		Payload: WebhookPayload{
			ID:     "msg-loc",
			Sender: SenderInfo{Contacts: []Contact{{IdentifierValue: "+573001234567"}}},
			Body:   MessageBody{Type: "location"},
		},
	}
	msg := ParseInboundMessage(event)
	if msg.MessageType != "location" {
		t.Errorf("expected location, got %s", msg.MessageType)
	}
}

func TestParseInboundMessage_Contact(t *testing.T) {
	event := WebhookEvent{
		Payload: WebhookPayload{
			ID:     "msg-contact",
			Sender: SenderInfo{Contacts: []Contact{{IdentifierValue: "+573001234567"}}},
			Body:   MessageBody{Type: "contacts"},
		},
	}
	msg := ParseInboundMessage(event)
	if msg.MessageType != "contact" {
		t.Errorf("expected contact, got %s", msg.MessageType)
	}
}

func TestParseInboundMessage_Sticker(t *testing.T) {
	event := WebhookEvent{
		Payload: WebhookPayload{
			ID:     "msg-sticker",
			Sender: SenderInfo{Contacts: []Contact{{IdentifierValue: "+573001234567"}}},
			Body:   MessageBody{Type: "sticker"},
		},
	}
	msg := ParseInboundMessage(event)
	if msg.MessageType != "sticker" {
		t.Errorf("expected sticker, got %s", msg.MessageType)
	}
}

func TestParseInboundMessage_Document(t *testing.T) {
	event := WebhookEvent{
		Payload: WebhookPayload{
			ID:     "msg-doc",
			Sender: SenderInfo{Contacts: []Contact{{IdentifierValue: "+573001234567"}}},
			Body:   MessageBody{Type: "document"},
		},
	}
	msg := ParseInboundMessage(event)
	if msg.MessageType != "document" {
		t.Errorf("expected document, got %s", msg.MessageType)
	}
}

func TestParseInboundMessage_UnknownType(t *testing.T) {
	event := WebhookEvent{
		Payload: WebhookPayload{
			ID:     "msg-unknown",
			Sender: SenderInfo{Contacts: []Contact{{IdentifierValue: "+573001234567"}}},
			Body:   MessageBody{Type: "reactions"},
		},
	}
	msg := ParseInboundMessage(event)
	if msg.MessageType != "reactions" {
		t.Errorf("expected reactions (passthrough), got %s", msg.MessageType)
	}
}

func TestParseInboundMessage_DisplayName(t *testing.T) {
	event := WebhookEvent{
		Payload: WebhookPayload{
			ID: "msg-dn",
			Sender: SenderInfo{
				DisplayName: "Carlos García",
				Contacts:    []Contact{{IdentifierValue: "+573001234567"}},
			},
			Body: MessageBody{Type: "text", Text: TextBody{Text: "hi"}},
		},
	}
	msg := ParseInboundMessage(event)
	if msg.DisplayName != "Carlos García" {
		t.Errorf("expected display name, got %s", msg.DisplayName)
	}
}
