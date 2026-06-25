package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Client sends messages via the Telegram Bot API.
type Client struct {
	httpClient *http.Client
	botToken   string
	chatID     string
}

// NewClient creates a Telegram client. Returns nil if token or chatID are empty.
func NewClient(botToken, chatID string) *Client {
	if botToken == "" || chatID == "" {
		return nil
	}
	return &Client{
		httpClient: &http.Client{Timeout: 10 * time.Second},
		botToken:   botToken,
		chatID:     chatID,
	}
}

// sendMessagePayload is the JSON body for Telegram sendMessage.
type sendMessagePayload struct {
	ChatID    string `json:"chat_id"`
	Text      string `json:"text"`
	ParseMode string `json:"parse_mode"`
}

// SendMessage sends a text message to the configured chat. HTML parse mode.
// Truncates to 4096 characters (Telegram limit).
func (c *Client) SendMessage(ctx context.Context, text string) error {
	if c == nil {
		return nil
	}

	// Telegram message limit
	runes := []rune(text)
	if len(runes) > 4096 {
		text = string(runes[:4093]) + "..."
	}

	payload := sendMessagePayload{
		ChatID:    c.chatID,
		Text:      text,
		ParseMode: "HTML",
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("telegram marshal: %w", err)
	}

	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", c.botToken)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("telegram request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		// El *url.Error de http.Do incluye la URL con el bot token embebido
		// (https://api.telegram.org/bot<TOKEN>/...). Se redacta el token antes
		// de propagar el error para que no se filtre a logs (p.ej. capacity.go).
		return fmt.Errorf("telegram send: %s", redactToken(err.Error(), c.botToken))
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("telegram status %d", resp.StatusCode)
	}
	return nil
}

// redactToken sustituye el bot token por "<redacted>" en cualquier mensaje de error
// (la URL de la API de Telegram lleva el token en el path /bot<TOKEN>/...).
func redactToken(msg, token string) string {
	if token == "" {
		return msg
	}
	return strings.ReplaceAll(msg, token, "<redacted>")
}
