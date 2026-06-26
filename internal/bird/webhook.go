package bird

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/utils"
)

// maxTimestampAge: ventana anti-replay. 24h en segundos. Cubre la ventana de REINTENTOS de Bird
// (~24h): un valor menor (antes 15 min) rechazaba los reintentos de webhooks que fallaron durante una
// caída/incidente, respondiéndoles 401 → Bird reintentaba el mismo payload viejo en bucle → mensajes de
// pacientes perdidos. Subirlo es seguro: el WAL deduplica por ID de mensaje (message_inbox, retenido
// 24h) → un replay con el mismo ID se descarta sin reprocesar; la firma sigue siendo obligatoria.
const maxTimestampAge = 24 * 60 * 60 // 24h en segundos

// VerifyWebhookSignature verifica la firma HMAC-SHA256 del webhook de Bird.
//
// Algoritmo Bird (docs oficiales + ejemplo Go):
//  1. actualSig = base64Decode(signatureHeader)
//  2. bodyHash  = SHA256(body) → [32]byte raw
//  3. payload   = fmt.Sprintf("%s\n%s\n%s", timestamp, requestURL, bodyHash)
//     Nota: %s sobre [32]byte produce bytes crudos, NO hex
//  4. expected  = HMAC-SHA256(signingKey, payload)
//  5. Comparar expected == actualSig
func (c *Client) VerifyWebhookSignature(signature, timestamp, requestURL string, body []byte) bool {
	return VerifySignatureWithKey(c.WebhookSecret, signature, timestamp, requestURL, body)
}

// VerifyOutboundWebhookSignature verifies using the outbound webhook's signing key.
func (c *Client) VerifyOutboundWebhookSignature(signature, timestamp, requestURL string, body []byte) bool {
	return VerifySignatureWithKey(c.WebhookSecretOutbound, signature, timestamp, requestURL, body)
}

// VerifySignatureWithKey verifies a Bird webhook HMAC-SHA256 signature with an explicit key.
func VerifySignatureWithKey(secret, signature, timestamp, requestURL string, body []byte) bool {
	if signature == "" || timestamp == "" {
		return false
	}

	// Replay attack prevention: reject timestamps older than maxTimestampAge (24h). El dedup por ID
	// del WAL evita reprocesar un replay; Bird reintenta ~24h, así que esta ventana los acepta.
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return false
	}
	if math.Abs(float64(time.Now().Unix()-ts)) > maxTimestampAge {
		return false
	}

	// Step 1: Decode the signature from the header
	actualSignature, err := base64.StdEncoding.DecodeString(signature)
	if err != nil {
		slog.Debug("webhook signature base64 decode failed", "error", err)
		return false
	}

	// Steps 2-4: Calculate the expected signature
	expectedSignature := signSha256(secret, timestamp, requestURL, body)

	match := hmac.Equal(expectedSignature, actualSignature)
	if !match {
		slog.Debug("webhook signature mismatch",
			"url", requestURL,
			"timestamp", timestamp,
			"expected_b64", base64.StdEncoding.EncodeToString(expectedSignature),
			"actual_b64", base64.StdEncoding.EncodeToString(actualSignature),
			"body_len", len(body),
			"secret_len", len(secret),
		)
	}

	// Step 5: Compare
	return match
}

// signSha256 calcula la firma Bird HMAC-SHA256.
// Replica exactamente el ejemplo oficial de Bird en Go.
func signSha256(signingKey, timestamp, requestURL string, data []byte) []byte {
	// Step 2: SHA256 hash del body como bytes crudos
	bh := sha256.Sum256(data)

	// Step 3: Concatenar timestamp + URL + bodyHash con newlines
	// Nota: %s sobre [32]byte escribe los 32 bytes raw, no hex
	var m bytes.Buffer
	fmt.Fprintf(&m, "%s\n%s\n%s", timestamp, requestURL, bh)

	// Step 4: HMAC-SHA256 con signing key
	mac := hmac.New(sha256.New, []byte(signingKey))
	mac.Write(m.Bytes())
	return mac.Sum(nil)
}

// ParseInboundMessage extrae un InboundMessage del payload crudo del webhook
func ParseInboundMessage(event WebhookEvent) InboundMessage {
	payload := event.Payload
	msg := InboundMessage{
		ID:             payload.ID,
		ConversationID: payload.ConversationID,
		ReceivedAt:     time.Now(),
	}

	// Extraer teléfono del sender (Bird usa "contact" singular, legacy usa "contacts" array)
	if payload.Sender.Contact.IdentifierValue != "" {
		msg.Phone = payload.Sender.Contact.IdentifierValue
		if name, ok := payload.Sender.Contact.Annotations["name"]; ok && name != "" {
			msg.DisplayName = name
		}
	} else if len(payload.Sender.Contacts) > 0 {
		msg.Phone = payload.Sender.Contacts[0].IdentifierValue
	}
	if msg.DisplayName == "" {
		msg.DisplayName = payload.Sender.DisplayName
	}

	slog.Debug("webhook_parsed",
		"message_id", msg.ID,
		"phone", utils.MaskPhone(msg.Phone),
		"conversation_id", msg.ConversationID,
		"direction", payload.Direction,
		"body_type", payload.Body.Type,
		"has_text_actions", len(payload.Body.Text.Actions) > 0,
		"has_list_actions", len(payload.Body.List.Actions) > 0,
		"has_interactive_actions", len(payload.Body.Interactive.Actions) > 0,
		"text_text", payload.Body.Text.Text,
		"list_text", payload.Body.List.Text,
		"interactive_text", payload.Body.Interactive.Text,
	)

	// Clasificar tipo de mensaje
	switch payload.Body.Type {
	case "text", "interactive", "list", "button":
		if postback, ok := ExtractPostbackPayload(payload.Body); ok {
			msg.MessageType = "postback"
			msg.IsPostback = true
			msg.PostbackPayload = postback
			msg.Text = postback
		} else {
			msg.MessageType = "text"
			// Bird may place text under body.text, body.list, or body.interactive
			msg.Text = payload.Body.Text.Text
			if msg.Text == "" {
				msg.Text = payload.Body.List.Text
			}
			if msg.Text == "" {
				msg.Text = payload.Body.Interactive.Text
			}
		}
	case "image":
		msg.MessageType = "image"
		if len(payload.Body.Image.Images) > 0 {
			msg.ImageURL = payload.Body.Image.Images[0].MediaURL
		}
		if msg.ImageURL == "" {
			msg.ImageURL = payload.Body.Text.Text // legacy fallback
		}
	case "file", "document":
		msg.MessageType = "document"
		if len(payload.Body.File.Files) > 0 {
			msg.DocumentURL = payload.Body.File.Files[0].MediaURL
		}
		if msg.DocumentURL == "" {
			msg.DocumentURL = payload.Body.Text.Text // legacy fallback
		}
	case "audio":
		msg.MessageType = "audio"
	case "video":
		msg.MessageType = "video"
	case "location":
		msg.MessageType = "location"
	case "contacts":
		msg.MessageType = "contact"
	case "sticker":
		msg.MessageType = "sticker"
	default:
		msg.MessageType = payload.Body.Type
	}

	return msg
}

// ExtractPostbackPayload extrae el payload de un postback.
// Bird may place the postback under body.text, body.list, or body.interactive
// depending on the original message type.
func ExtractPostbackPayload(body MessageBody) (string, bool) {
	// Check body.text.actions (buttons and some postback responses)
	if len(body.Text.Actions) > 0 && body.Text.Actions[0].Type == "postback" {
		return body.Text.Actions[0].Postback.Payload, true
	}
	// Check body.list.actions (list selection responses)
	if len(body.List.Actions) > 0 && body.List.Actions[0].Type == "postback" {
		return body.List.Actions[0].Postback.Payload, true
	}
	// Check body.interactive.actions (interactive message responses)
	if len(body.Interactive.Actions) > 0 && body.Interactive.Actions[0].Type == "postback" {
		return body.Interactive.Actions[0].Postback.Payload, true
	}
	return "", false
}
