package bird

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/neuro-bot/neuro-bot/internal/utils"
)

// Plantillas PROACTIVAS: las que el bot manda sin que el paciente haya escrito (recordatorios, lista
// de espera, avisos administrativos). En Bird basta con mandarlas al teléfono; en Néctar el bot tiene
// que ABRIR la conversación con la plantilla (POST /api/bot/conversations/open) y, desde ahí, el
// paciente contesta en la sesión "nectar:<id>", no en su teléfono. SendProactiveTemplate es la única
// puerta de esos envíos y devuelve la clave con la que se registra el pending.

// ProactiveSend es el resultado de una plantilla proactiva. Key es la clave de la sesión que recibirá
// la respuesta: el mismo teléfono en Bird, "nectar:<id>" en Néctar.
type ProactiveSend struct {
	MessageID string
	Key       string
}

// NectarConversationID devuelve el id de la conversación de Néctar ("77" de "nectar:77"); "" si el
// envío fue por Bird, y entonces quien llama resuelve la conversación de Bird como siempre.
func (s ProactiveSend) NectarConversationID() string {
	if id, ok := strings.CutPrefix(s.Key, nectarPhonePrefix); ok {
		return id
	}
	return ""
}

// ContactPhone es el teléfono del paciente que acompaña al pending cuando la clave NO es el
// teléfono (Néctar); "" cuando lo es (Bird, o una conversación de Néctar ya identificada por su clave).
func (s ProactiveSend) ContactPhone(phone string) string {
	if s.Key == phone {
		return ""
	}
	return phone
}

// ErrConversationWithAgent es el 409 already_open con mine:false: el contacto ya conversa con un
// agente (o está en la cola) en Néctar. El bot no le escribe encima; quien llama lo salta sin pending.
var ErrConversationWithAgent = fmt.Errorf("el contacto ya conversa con un agente en Néctar: %w", ErrPermanentSend)

// AlreadyOpenError es el 409 already_open de la apertura: el contacto ya tiene una conversación viva
// en ese canal. Mine dice de quién es: true = del bot (sigue en ella, ConversationID), false = de un
// agente o en cola (no se le escribe). Envuelve ErrPermanentSend: reabrir no cambia la respuesta.
//
// Se declara aquí y no en internal/nectar (que la nombra con un alias) por la misma razón que
// Escalation: SendProactiveTemplate tiene que reconocerla, y este paquete no importa al de Néctar.
type AlreadyOpenError struct {
	ConversationID int64
	Mine           bool
}

func (e *AlreadyOpenError) Error() string {
	return fmt.Sprintf("nectar: el contacto ya tiene conversación abierta (409 already_open, conversation_id=%d, mine=%t)",
		e.ConversationID, e.Mine)
}

// Unwrap la marca como permanente para cualquier envoltorio de reintentos.
func (e *AlreadyOpenError) Unwrap() error { return ErrPermanentSend }

// SendProactiveTemplate manda una plantilla que inicia la conversación con el paciente.
//
//   - Sin puente: es SendTemplate tal cual (la petición a Bird no cambia) y Key es el teléfono.
//   - Con puente: abre la conversación en Néctar con la plantilla (una sola llamada: la plantilla
//     viaja DENTRO de la apertura) y Key es "nectar:<id>". Si el contacto ya tiene conversación del
//     bot (409 mine:true), la plantilla va a esa; si es de un agente, ErrConversationWithAgent.
//
// El kill switch y el tope por destinatario se aplican ANTES de abrir, como en SendTemplate. Un error
// de la apertura no se reintenta aquí ni debe reintentarlo quien llama: ErrOpenInDoubt (o un corte
// de red con la petición ya enviada) puede significar que la plantilla salió, y otro intento acuñaría
// otra clave de idempotencia y llegaría dos veces.
func (c *Client) SendProactiveTemplate(ctx context.Context, phone string, tmpl TemplateConfig) (ProactiveSend, error) {
	// Sin puente, o con una clave de sesión de Néctar ya en la mano: el camino de siempre.
	if _, ok := c.nectarConversation(phone, ""); ok || c.nectar == nil {
		id, err := c.SendTemplate(phone, tmpl) //nolint:contextcheck // SendTemplate no toma ctx: el camino de Bird queda tal cual
		if err != nil {
			return ProactiveSend{}, err
		}
		return ProactiveSend{MessageID: id, Key: phone}, nil
	}
	if c.notifWADisabled {
		return ProactiveSend{}, ErrWhatsAppNotificationsDisabled
	}
	// La apertura solo direcciona por teléfono: un username/BSUID se rechazaría con un 4xx. Se
	// corta ANTES de cualquier llamada, con el mismo error que el gate E.164 de Bird.
	if !utils.IsE164(phone) {
		return ProactiveSend{}, ErrNonContactable
	}
	if !c.allowSend(phone) {
		return ProactiveSend{}, ErrSendRateLimited
	}

	convID, msgID, err := c.nectar.OpenWithTemplate(ctx, phone, tmpl)
	var already *AlreadyOpenError
	if errors.As(err, &already) {
		if !already.Mine {
			return ProactiveSend{}, fmt.Errorf("%w (conversation_id=%d)", ErrConversationWithAgent, already.ConversationID)
		}
		if already.ConversationID <= 0 {
			// 409 mine:true sin id legible: no hay a qué conversación escribir.
			return ProactiveSend{}, fmt.Errorf("nectar: already_open sin conversation_id: %w", err)
		}
		id, err := c.nectar.SendTemplate(ctx, already.ConversationID, tmpl)
		if err != nil {
			return ProactiveSend{}, err
		}
		return ProactiveSend{MessageID: id, Key: nectarKey(already.ConversationID)}, nil
	}
	if err != nil {
		return ProactiveSend{}, err
	}
	return ProactiveSend{MessageID: msgID, Key: nectarKey(convID)}, nil
}

// nectarKey arma la clave de sesión "nectar:<id>" (la misma que nectar.SessionKey).
func nectarKey(conversationID int64) string {
	return nectarPhonePrefix + strconv.FormatInt(conversationID, 10)
}

// ProactiveConversationID es la conversación a la que quedó atado el envío: la de Néctar si la
// plantilla abrió (o reusó) una allá; si fue por Bird, la de siempre — caché y, si no, el lookup por
// teléfono (visibilidad en el Inbox de Bird). El lookup de Bird nunca corre para un envío de Néctar.
func (c *Client) ProactiveConversationID(phone string, sent ProactiveSend) string {
	if id := sent.NectarConversationID(); id != "" {
		return id
	}
	convID := c.GetCachedConversationID(phone)
	if convID == "" {
		convID, _ = c.LookupConversationByPhone(phone)
	}
	return convID
}
