package nectar

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/neuro-bot/neuro-bot/internal/bird"
)

// La apertura con plantilla: POST /api/bot/conversations/open (Plan 44 §1.1 de Néctar). Es la
// única ruta por la que el bot INICIA una conversación con un paciente que no ha escrito
// (recordatorios, lista de espera, avisos administrativos). Sus códigos no significan lo mismo
// que en las rutas de siempre —el 404 es «canal o BSUID ajeno», no «ya no es mía», y el 503 con
// Retry-After es «saturado, vuelve»—, por eso tiene su propia traducción (doOpen) sobre el mismo
// armado de la petición (exchange).

var (
	// ErrOpenInDoubt dice que no se sabe si la plantilla salió, o salió y Néctar no dejó el hilo
	// (409 attempt_in_doubt / attempt_sent_unrecorded, 502 meta_no_answer / sent_but_not_recorded).
	// NO SE REINTENTA, ni con la misma clave ni con otra: podría llegarle dos veces al paciente.
	ErrOpenInDoubt = fmt.Errorf("nectar: apertura en duda, la plantilla pudo haber salido: %w", bird.ErrPermanentSend)
	// ErrTemplateRejected dice que la plantilla no sirve tal como se pidió (400 template_parameters,
	// 409 template_not_offerable / template_other_account, 502 meta_rejected). Es configuración:
	// reintentar falla igual.
	ErrTemplateRejected = fmt.Errorf("nectar: plantilla rechazada en la apertura: %w", bird.ErrPermanentSend)
	// ErrOpenNotFound es el 404 ciego de la apertura: el canal no es de la entidad de la clave o
	// el BSUID no tiene ficha. NO es ErrNotMine: aquí no hay ningún caso que soltar.
	ErrOpenNotFound = fmt.Errorf("nectar: canal o destinatario ajeno en la apertura (404): %w", bird.ErrPermanentSend)
	// ErrOpenNotConfigured dice que el puente no tiene canal de apertura (NECTAR_CHANNEL_ID = 0). Nada sale.
	ErrOpenNotConfigured = fmt.Errorf("nectar: apertura sin canal configurado (NECTAR_CHANNEL_ID): %w", bird.ErrPermanentSend)
)

// AlreadyOpenError es el 409 already_open (ConversationID, Mine). El tipo vive en el paquete bird
// —bird.Client.SendProactiveTemplate tiene que reconocerlo y ese paquete no importa a este—; aquí
// queda el alias para que la apertura lo nombre como siempre.
type AlreadyOpenError = bird.AlreadyOpenError

// OpenRecipient es el destinatario: EXACTAMENTE UNO de los dos (Néctar rechaza ninguno y ambos).
type OpenRecipient struct {
	Phone string `json:"phone,omitempty"`
	BSUID string `json:"bsuid,omitempty"`
}

// Opened es el 201 de la apertura.
type Opened struct {
	ConversationID int64 `json:"conversation_id"`
	ChannelID      int64 `json:"channel_id"`
	MessageID      int64 `json:"message_id"`
}

type openRequest struct {
	IdempotencyKey string        `json:"idempotency_key"`
	ChannelID      int64         `json:"channel_id"`
	Recipient      OpenRecipient `json:"recipient"`
	Template       TemplateBody  `json:"template"`
	// Name es opcional; Néctar solo lo escribe en un contacto sin nombre.
	Name string `json:"name,omitempty"`
}

const (
	openPath = "/api/bot/conversations/open"
	// openMaxRetries: como mucho 2 reintentos, y solo ante 503 con Retry-After.
	openMaxRetries = 2
	// openDefaultRetryAfter es la espera si la cabecera viene pero no se entiende.
	openDefaultRetryAfter = 2 * time.Second
)

// OpenConversation abre una conversación con una plantilla. La idempotency_key se acuña UNA vez
// por llamada y se reusa en sus reintentos: así un reintento tras un corte no manda dos plantillas
// (Néctar contesta already_open con mine=true).
func (c *Client) OpenConversation(ctx context.Context, channelID int64, to OpenRecipient, tmpl TemplateBody,
	name string,
) (Opened, error) {
	req := openRequest{
		IdempotencyKey: uuid.NewString(),
		ChannelID:      channelID,
		Recipient:      to,
		Template:       tmpl,
		Name:           name,
	}
	for attempt := 0; ; attempt++ {
		resp, err := c.exchange(ctx, http.MethodPost, openPath, req)
		if err != nil {
			// Fallo de transporte: no se reintenta aquí (la regla solo admite el 503).
			return Opened{}, err
		}
		wait, retry := openRetryAfter(resp)
		if retry && attempt < openMaxRetries {
			if err := c.sleep(ctx, wait); err != nil {
				return Opened{}, err
			}
			continue
		}
		return decodeOpen(resp)
	}
}

// openRetryAfter dice si la respuesta es el 503 reintentable y cuánto esperar. El 503 SIN
// Retry-After es «Néctar arrancó sin cliente de Meta»: reintentar falla igual.
func openRetryAfter(resp reply) (time.Duration, bool) {
	if resp.status != http.StatusServiceUnavailable {
		return 0, false
	}
	h := strings.TrimSpace(resp.header.Get("Retry-After"))
	if h == "" {
		return 0, false
	}
	secs, err := strconv.Atoi(h)
	if err != nil || secs < 0 {
		return openDefaultRetryAfter, true
	}
	return time.Duration(secs) * time.Second, true
}

// decodeOpen traduce la respuesta final de la apertura. El cuerpo de un rechazo no se copia al
// error: solo el código y el reason (nunca el teléfono).
func decodeOpen(resp reply) (Opened, error) {
	reason := replyReason(resp.body)
	switch resp.status {
	case http.StatusCreated, http.StatusOK:
		var out Opened
		if err := json.Unmarshal(resp.body, &out); err != nil {
			return Opened{}, fmt.Errorf("nectar: leer respuesta de %s: %w", openPath, err)
		}
		return out, nil
	case http.StatusNotFound:
		return Opened{}, ErrOpenNotFound
	case http.StatusUnauthorized:
		return Opened{}, ErrUnauthorized
	case http.StatusConflict:
		switch reason {
		case "already_open":
			var e struct {
				ConversationID int64 `json:"conversation_id"`
				Mine           bool  `json:"mine"`
			}
			_ = json.Unmarshal(resp.body, &e)
			return Opened{}, &AlreadyOpenError{ConversationID: e.ConversationID, Mine: e.Mine}
		case "attempt_in_doubt", "attempt_sent_unrecorded":
			return Opened{}, fmt.Errorf("%w (409 %s)", ErrOpenInDoubt, reason)
		case "idempotency_key_reused":
			return Opened{}, ErrIdempotencyReused
		case "template_not_offerable", "template_other_account":
			return Opened{}, fmt.Errorf("%w (409 %s)", ErrTemplateRejected, reason)
		}
	case http.StatusBadRequest:
		if reason == "template_parameters" {
			return Opened{}, fmt.Errorf("%w (400 %s)", ErrTemplateRejected, reason)
		}
	case http.StatusBadGateway:
		if reason == "meta_rejected" {
			return Opened{}, fmt.Errorf("%w (502 %s)", ErrTemplateRejected, reason)
		}
		// Todo 502 es «en duda», con cualquier motivo o sin cuerpo (un 502 de un proxy delante de
		// Néctar incluido): la plantilla pudo llegar a Meta, y un reintento de quien llama acuñaría
		// otra clave y Néctar mandaría una SEGUNDA plantilla.
		return Opened{}, fmt.Errorf("%w (502 %q)", ErrOpenInDoubt, reason)
	}
	// 4xx con otro motivo (channel_inactive, phone_invalid…) es un defecto de configuración o del
	// cuerpo: permanente. 5xx sin motivo conocido (503 agotado incluido) se deja transitorio.
	if resp.status >= 400 && resp.status < 500 {
		return Opened{}, fmt.Errorf("nectar: POST %s: status %d reason %q: %w", openPath, resp.status, reason, bird.ErrPermanentSend)
	}
	return Opened{}, fmt.Errorf("nectar: POST %s: status %d reason %q", openPath, resp.status, reason)
}
