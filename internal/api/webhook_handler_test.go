package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/config"
	"github.com/neuro-bot/neuro-bot/internal/worker"
)

func TestIsNotificationPostback_True(t *testing.T) {
	truePayloads := []string{"confirm", "cancelar", "cancel", "understood", "reschedule", "wl_schedule", "wl_decline"}
	for _, p := range truePayloads {
		if !IsNotificationPostback(p) {
			t.Errorf("expected true for %q", p)
		}
	}
}

func TestIsNotificationPostback_False(t *testing.T) {
	falsePayloads := []string{"consultar", "agendar", "agente", "identity_yes", "otra_cita", "", "random"}
	for _, p := range falsePayloads {
		if IsNotificationPostback(p) {
			t.Errorf("expected false for %q", p)
		}
	}
}

// computeTestBirdSignature replica el algoritmo exacto de firma de Bird:
// bh = SHA256(body) → raw [32]byte
// payload = fmt.Sprintf("%s\n%s\n%s", timestamp, url, bh) → %s = raw bytes
// sig = base64(HMAC-SHA256(key, payload))
func computeTestBirdSignature(secret, timestamp, fullURL string, body []byte) string {
	bh := sha256.Sum256(body)
	var m bytes.Buffer
	fmt.Fprintf(&m, "%s\n%s\n%s", timestamp, fullURL, bh)

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(m.Bytes())
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// signedRequest creates a request with valid Bird signature headers.
// httptest uses "example.com" as default Host, so full URL = https://example.com + path
func signedRequest(method, path string, body []byte, secret string) *http.Request {
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	// reconstructFullURL will produce: https://example.com + path
	fullURL := "https://example.com" + path
	sig := computeTestBirdSignature(secret, timestamp, fullURL, body)

	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("MessageBird-Signature", sig)
	req.Header.Set("MessageBird-Request-Timestamp", timestamp)
	return req
}

func TestHandleWhatsApp_InvalidSignature(t *testing.T) {
	birdClient := &bird.Client{WebhookSecret: "test-secret"}
	pool := worker.NewMessageWorkerPool(1, 10)
	h := NewWebhookHandler(birdClient, pool, nil, &config.Config{})

	body := []byte(`{"payload":{}}`)
	req := httptest.NewRequest("POST", "/api/webhooks/whatsapp", bytes.NewReader(body))
	req.Header.Set("MessageBird-Signature", "aW52YWxpZC1zaWduYXR1cmU=")
	req.Header.Set("MessageBird-Request-Timestamp", "1700000000")
	rec := httptest.NewRecorder()

	h.HandleWhatsApp(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for invalid signature, got %d", rec.Code)
	}
}

func TestHandleWhatsApp_MissingSignature(t *testing.T) {
	birdClient := &bird.Client{WebhookSecret: "test-secret"}
	pool := worker.NewMessageWorkerPool(1, 10)
	h := NewWebhookHandler(birdClient, pool, nil, &config.Config{})

	body := []byte(`{"payload":{}}`)
	req := httptest.NewRequest("POST", "/api/webhooks/whatsapp", bytes.NewReader(body))
	// No signature headers
	rec := httptest.NewRecorder()

	h.HandleWhatsApp(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for missing signature, got %d", rec.Code)
	}
}

func TestHandleWhatsApp_OutboundIgnored(t *testing.T) {
	secret := "test-secret"
	birdClient := &bird.Client{WebhookSecret: secret}
	pool := worker.NewMessageWorkerPool(1, 10)
	h := NewWebhookHandler(birdClient, pool, nil, &config.Config{})

	// Test both "outbound" (legacy) and "outgoing" (Bird actual)
	for _, dir := range []string{"outbound", "outgoing"} {
		event := bird.WebhookEvent{
			Payload: bird.WebhookPayload{
				Direction: dir,
				ID:        "msg-out",
			},
		}
		body, _ := json.Marshal(event)
		req := signedRequest("POST", "/api/webhooks/whatsapp", body, secret)
		rec := httptest.NewRecorder()

		h.HandleWhatsApp(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("[%s] expected 200, got %d", dir, rec.Code)
		}
		size, _ := pool.QueueStats()
		if size != 0 {
			t.Errorf("[%s] expected 0 enqueued messages for outbound, got %d", dir, size)
		}
	}
}

func TestHandleWhatsApp_InboundText_Enqueued(t *testing.T) {
	secret := "test-secret"
	birdClient := &bird.Client{WebhookSecret: secret}
	pool := worker.NewMessageWorkerPool(1, 50)
	h := NewWebhookHandler(birdClient, pool, nil, &config.Config{})

	event := bird.WebhookEvent{
		Payload: bird.WebhookPayload{
			ID:        "msg-in-1",
			Direction: "inbound",
			Sender: bird.SenderInfo{
				Contacts: []bird.Contact{{IdentifierValue: "+573001234567"}},
			},
			Body: bird.MessageBody{
				Type: "text",
				Text: bird.TextBody{Text: "Hola"},
			},
		},
	}
	body, _ := json.Marshal(event)
	req := signedRequest("POST", "/api/webhooks/whatsapp", body, secret)
	rec := httptest.NewRecorder()

	h.HandleWhatsApp(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	size, _ := pool.QueueStats()
	if size != 1 {
		t.Errorf("expected 1 enqueued message, got %d", size)
	}
}

func TestHandleWhatsAppOutbound_AgentCommand(t *testing.T) {
	secret := "test-secret"
	birdClient := &bird.Client{WebhookSecret: secret, WebhookSecretOutbound: secret}
	pool := worker.NewMessageWorkerPool(1, 50)
	h := NewWebhookHandler(birdClient, pool, nil, &config.Config{})

	// Test with Bird actual format: "outgoing" direction + Connector (singular)
	event := bird.WebhookEvent{
		Payload: bird.WebhookPayload{
			ID:        "msg-out-1",
			Direction: "outgoing",
			Receiver: bird.ReceiverInfo{
				Connector: bird.Contact{IdentifierValue: "+573001234567"},
			},
			Body: bird.MessageBody{
				Type: "text",
				Text: bird.TextBody{Text: "/bot resume ASK_DOCUMENT"},
			},
		},
	}
	body, _ := json.Marshal(event)
	req := signedRequest("POST", "/api/webhooks/whatsapp/outbound", body, secret)
	rec := httptest.NewRecorder()

	h.HandleWhatsAppOutbound(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestHandleWhatsAppOutbound_AgentCommand_LegacyFormat(t *testing.T) {
	secret := "test-secret"
	birdClient := &bird.Client{WebhookSecret: secret, WebhookSecretOutbound: secret}
	pool := worker.NewMessageWorkerPool(1, 50)
	h := NewWebhookHandler(birdClient, pool, nil, &config.Config{})

	// Test with legacy format: "outbound" direction + Contacts array
	event := bird.WebhookEvent{
		Payload: bird.WebhookPayload{
			ID:        "msg-out-2",
			Direction: "outbound",
			Receiver: bird.ReceiverInfo{
				Contacts: []bird.Contact{{IdentifierValue: "+573001234567"}},
			},
			Body: bird.MessageBody{
				Type: "text",
				Text: bird.TextBody{Text: "/bot resume ASK_DOCUMENT"},
			},
		},
	}
	body, _ := json.Marshal(event)
	req := signedRequest("POST", "/api/webhooks/whatsapp/outbound", body, secret)
	rec := httptest.NewRecorder()

	h.HandleWhatsAppOutbound(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestHandleWhatsAppOutbound_InboundIgnored(t *testing.T) {
	secret := "test-secret"
	birdClient := &bird.Client{WebhookSecret: secret, WebhookSecretOutbound: secret}
	pool := worker.NewMessageWorkerPool(1, 50)
	h := NewWebhookHandler(birdClient, pool, nil, &config.Config{})

	// Both "inbound" (legacy) and "incoming" (Bird actual) should be ignored
	for _, dir := range []string{"inbound", "incoming"} {
		event := bird.WebhookEvent{
			Payload: bird.WebhookPayload{
				ID:        "msg-in-1",
				Direction: dir,
				Sender: bird.SenderInfo{
					Contacts: []bird.Contact{{IdentifierValue: "+573001234567"}},
				},
				Body: bird.MessageBody{
					Type: "text",
					Text: bird.TextBody{Text: "Hola"},
				},
			},
		}
		body, _ := json.Marshal(event)
		req := signedRequest("POST", "/api/webhooks/whatsapp/outbound", body, secret)
		rec := httptest.NewRecorder()

		h.HandleWhatsAppOutbound(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("[%s] expected 200, got %d", dir, rec.Code)
		}
		size, _ := pool.QueueStats()
		if size != 0 {
			t.Errorf("[%s] expected 0 enqueued messages, got %d", dir, size)
		}
	}
}

func TestHandleWhatsApp_EmptyBody(t *testing.T) {
	birdClient := &bird.Client{WebhookSecret: "secret"}
	pool := worker.NewMessageWorkerPool(1, 10)
	h := NewWebhookHandler(birdClient, pool, nil, &config.Config{})

	req := httptest.NewRequest("POST", "/api/webhooks/whatsapp", bytes.NewReader(nil))
	rec := httptest.NewRecorder()

	h.HandleWhatsApp(rec, req)

	// Empty body → signature will be empty → 401
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for empty body, got %d", rec.Code)
	}
}

func TestReconstructFullURL(t *testing.T) {
	// Default httptest host
	req := httptest.NewRequest("POST", "/api/webhooks/whatsapp", nil)
	got := reconstructFullURL(req)
	if got != "https://example.com/api/webhooks/whatsapp" {
		t.Errorf("expected https://example.com/api/webhooks/whatsapp, got %s", got)
	}

	// With X-Forwarded headers (ngrok)
	req2 := httptest.NewRequest("POST", "/api/webhooks/whatsapp", nil)
	req2.Header.Set("X-Forwarded-Proto", "https")
	req2.Header.Set("X-Forwarded-Host", "mybot.ngrok-free.dev")
	got2 := reconstructFullURL(req2)
	if got2 != "https://mybot.ngrok-free.dev/api/webhooks/whatsapp" {
		t.Errorf("expected ngrok URL, got %s", got2)
	}
}

// ---------------------------------------------------------------------------
// Identificadores no persistibles (H138-2/H139-1): un identifierValue
// whatsappusername (>20 chars) no cabe en message_inbox.phone VARCHAR(20) →
// 1406 → el handler respondía 500 → Bird reintentaba ETERNO (36 vueltas/16h por
// mensaje) y cada vuelta era una alerta Telegram. Un error de FORMA de dato es
// PERMANENTE: reintentarlo es garantía de bucle.
// ---------------------------------------------------------------------------

// mockInbox implementa InboxPersister devolviendo el error configurado.
type mockInbox struct {
	err     error
	calls   int
	lastLen int
}

func (m *mockInbox) InsertIfNotExists(_ context.Context, _, phone, _, _ string, _ time.Time) (bool, error) {
	m.calls++
	m.lastLen = len(phone)
	if m.err != nil {
		return false, m.err
	}
	return true, nil
}

func inboundEvent(id, identifier string) []byte {
	event := bird.WebhookEvent{
		Payload: bird.WebhookPayload{
			ID:        id,
			Direction: "incoming",
			Sender: bird.SenderInfo{
				Contacts: []bird.Contact{{IdentifierValue: identifier}},
			},
			Body: bird.MessageBody{
				Type: "text",
				Text: bird.TextBody{Text: "Hola"},
			},
		},
	}
	body, _ := json.Marshal(event)
	return body
}

// TestHandleWhatsApp_OversizedIdentifierDroppedAtEdge: un identificador que excede la
// capacidad de la tubería (VARCHAR(20) en message_inbox, sessions, chat_events…) se
// descarta EN EL BORDE con 200: ni WAL, ni encolado, ni 500 que dispare reintentos.
func TestHandleWhatsApp_OversizedIdentifierDroppedAtEdge(t *testing.T) {
	secret := "test-secret"
	birdClient := &bird.Client{WebhookSecret: secret}
	pool := worker.NewMessageWorkerPool(1, 10)
	h := NewWebhookHandler(birdClient, pool, nil, &config.Config{})
	inbox := &mockInbox{}
	h.SetInboxRepo(inbox)

	body := inboundEvent("msg-oversized", "CO.a1b2c3d4e5f6g7h8i9j0k1l2m3n4") // 30 chars
	req := signedRequest("POST", "/api/webhooks/whatsapp", body, secret)
	rec := httptest.NewRecorder()

	h.HandleWhatsApp(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("identificador sobredimensionado debe ACK 200 (drop en el borde), got %d", rec.Code)
	}
	if inbox.calls != 0 {
		t.Errorf("no debe intentar el WAL con un identificador que no cabe, got %d llamadas", inbox.calls)
	}
	if size, _ := pool.QueueStats(); size != 0 {
		t.Errorf("no debe encolarse, got %d", size)
	}
}

// TestHandleWhatsApp_InboxPersist1406_AckedAsPermanent: si aún así el INSERT devuelve
// 1406 (Data too long, cualquier columna), es permanente → 200 a Bird, sin reintento.
func TestHandleWhatsApp_InboxPersist1406_AckedAsPermanent(t *testing.T) {
	secret := "test-secret"
	birdClient := &bird.Client{WebhookSecret: secret}
	pool := worker.NewMessageWorkerPool(1, 10)
	h := NewWebhookHandler(birdClient, pool, nil, &config.Config{})
	h.SetInboxRepo(&mockInbox{err: &mysql.MySQLError{Number: 1406, Message: "Data too long for column 'phone' at row 1"}})

	body := inboundEvent("msg-1406", "+573001234567")
	req := signedRequest("POST", "/api/webhooks/whatsapp", body, secret)
	rec := httptest.NewRecorder()

	h.HandleWhatsApp(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("1406 es permanente: debe ACK 200 (cortar el reintento eterno de Bird), got %d", rec.Code)
	}
	if size, _ := pool.QueueStats(); size != 0 {
		t.Errorf("mensaje no persistido no debe encolarse, got %d", size)
	}
}

// TestHandleWhatsApp_InboxPersistTransient_Still500: un error transitorio real de BD
// SIGUE respondiendo 500 — ahí el reintento de Bird es el fail-safe correcto.
func TestHandleWhatsApp_InboxPersistTransient_Still500(t *testing.T) {
	secret := "test-secret"
	birdClient := &bird.Client{WebhookSecret: secret}
	pool := worker.NewMessageWorkerPool(1, 10)
	h := NewWebhookHandler(birdClient, pool, nil, &config.Config{})
	h.SetInboxRepo(&mockInbox{err: fmt.Errorf("db down")})

	body := inboundEvent("msg-transient", "+573001234567")
	req := signedRequest("POST", "/api/webhooks/whatsapp", body, secret)
	rec := httptest.NewRecorder()

	h.HandleWhatsApp(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("error transitorio de BD debe seguir en 500 (fail-safe de reintento), got %d", rec.Code)
	}
}
