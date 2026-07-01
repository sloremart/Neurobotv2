package recovery

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Constantes fijas de la capa de recuperación (no configurables por env, son detalles internos):
// el modelo (decisión fija, distinto al del OCR) y el tope de tokens de salida (JSON corto).
const (
	DefaultModel           = "gpt-4.1-nano"
	DefaultMaxOutputTokens = 200
)

// Usage acumula el consumo de tokens de una recuperación (para KPIs/costo, sin PII).
type Usage struct {
	PromptTokens     int
	CompletionTokens int
	Calls            int
}

// LLMClient llama al endpoint chat/completions de OpenAI. Reutiliza el patrón HTTP del OCR
// (http.Client crudo, sin SDK) pero con su PROPIO modelo (AI_RECOVERY_MODEL = gpt-4.1-nano,
// distinto al del OCR). Salida en JSON mode y max_tokens bajo (§7.1).
type LLMClient struct {
	apiKey    string
	model     string
	apiURL    string
	http      *http.Client
	maxTokens int
	backoffs  []time.Duration // espera entre reintentos: 0.5s → 1s → 2s
}

// NewLLMClient crea el cliente. apiURL vacío usa el endpoint público de OpenAI.
func NewLLMClient(apiKey, model string, maxTokens int) *LLMClient {
	if maxTokens <= 0 {
		maxTokens = 200
	}
	return &LLMClient{
		apiKey:    apiKey,
		model:     model,
		apiURL:    "https://api.openai.com/v1/chat/completions",
		http:      &http.Client{Timeout: 20 * time.Second},
		maxTokens: maxTokens,
		backoffs:  []time.Duration{500 * time.Millisecond, 1 * time.Second, 2 * time.Second},
	}
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model          string            `json:"model"`
	Messages       []chatMessage     `json:"messages"`
	MaxTokens      int               `json:"max_tokens"`
	Temperature    float64           `json:"temperature"`
	ResponseFormat map[string]string `json:"response_format"`
}

type chatResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

// Complete envía (system,user) y parsea la respuesta JSON a Decision. Reintenta ante error/timeout
// con backoff (máx len(backoffs) reintentos); si todos fallan, retorna el último error.
func (c *LLMClient) Complete(ctx context.Context, system, user string) (Decision, Usage, error) {
	reqBody := chatRequest{
		Model: c.model,
		Messages: []chatMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
		MaxTokens:      c.maxTokens,
		Temperature:    0,
		ResponseFormat: map[string]string{"type": "json_object"},
	}
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return Decision{}, Usage{}, fmt.Errorf("marshal request: %w", err)
	}

	var lastErr error
	attempts := len(c.backoffs)
	if attempts < 1 {
		attempts = 1
	}
	for i := 0; i < attempts; i++ {
		if i > 0 {
			if !sleepCtx(ctx, c.backoffs[i-1]) {
				return Decision{}, Usage{}, ctx.Err()
			}
		}
		dec, usage, err := c.doRequest(ctx, payload)
		if err == nil {
			usage.Calls = i + 1
			return dec, usage, nil
		}
		lastErr = err
	}
	return Decision{}, Usage{Calls: attempts}, fmt.Errorf("llm complete after %d attempts: %w", attempts, lastErr)
}

func (c *LLMClient) doRequest(ctx context.Context, payload []byte) (Decision, Usage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiURL, bytes.NewReader(payload))
	if err != nil {
		return Decision{}, Usage{}, fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return Decision{}, Usage{}, fmt.Errorf("http do: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return Decision{}, Usage{}, fmt.Errorf("openai status %d", resp.StatusCode)
	}

	var parsed chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return Decision{}, Usage{}, fmt.Errorf("decode response: %w", err)
	}
	usage := Usage{PromptTokens: parsed.Usage.PromptTokens, CompletionTokens: parsed.Usage.CompletionTokens}
	if len(parsed.Choices) == 0 {
		return Decision{}, usage, fmt.Errorf("openai: empty choices")
	}

	var dec Decision
	if err := json.Unmarshal([]byte(parsed.Choices[0].Message.Content), &dec); err != nil {
		return Decision{}, usage, fmt.Errorf("decode decision json: %w", err)
	}
	return dec, usage, nil
}

// sleepCtx duerme d respetando la cancelación del contexto. Retorna false si el contexto se cancela.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
