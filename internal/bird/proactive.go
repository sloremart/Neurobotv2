package bird

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/neuro-bot/neuro-bot/internal/monitor"
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

// ErrProactiveConfig marca los errores de CONFIGURACIÓN de la apertura en Néctar: la clave de
// servicio, la plantilla, el canal o la clave de idempotencia no sirven, y fallarían igual con
// cualquier paciente. Los errores tipados de internal/nectar lo envuelven (este paquete no importa
// al de Néctar: la marca vive aquí, como AlreadyOpenError). Envuelve ErrPermanentSend: nada de esto
// se arregla reintentando.
var ErrProactiveConfig = fmt.Errorf("configuración de Néctar para plantillas proactivas: %w", ErrPermanentSend)

// IsProactiveConfigError dice si err es de configuración de la cuenta y no del paciente. Quien
// recorre una lista de pacientes se DETIENE en el primero: seguir sería una petición que no puede
// entregar por cada paciente, y marcarlos no contactables vaciaría la lista por un error de la cuenta.
func IsProactiveConfigError(err error) bool {
	return errors.Is(err, ErrProactiveConfig)
}

// ProactiveStopReporter es lo que ReportProactiveConfigStop necesita del aviso urgente de volver a
// Bird (monitor.UrgentAlerter).
type ProactiveStopReporter interface {
	Fire(ctx context.Context, trigger monitor.UrgentTrigger, detail string)
}

// ReportProactiveConfigStop deja el ÚNICO error de una corrida de plantillas proactivas que se detuvo
// por configuración (no uno por paciente): qué corrida, el error y cuántos quedaron sin enviar
// (unsent cuenta al paciente en el que se detuvo); y dispara el aviso urgente de volver a Bird. La
// comparten los tres lotes (avisos administrativos, recordatorios, lista de espera). urgent nil (sin
// Telegram o con el puente apagado) deja solo el error. El detalle no lleva teléfonos: la corrida,
// cuántos quedaron y la clase del error de Néctar.
func ReportProactiveConfigStop(ctx context.Context, urgent ProactiveStopReporter, run string, err error, unsent int) {
	slog.Error("plantillas proactivas detenidas: error de configuración de Néctar, nada más sale en esta corrida",
		"run", run, "error", err, "unsent", unsent)
	if urgent == nil {
		return
	}
	urgent.Fire(ctx, monitor.TriggerProactiveStopped,
		fmt.Sprintf("corrida %s detenida por configuración de Néctar; unsent=%d; %v", run, unsent, err))
}

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
//   - Sin puente: es SendTemplate tal cual (la petición a Bird no cambia) y Key es el teléfono. Una
//     clave "nectar:<id>" sin puente no tiene canal: SendTemplate devuelve ErrNectarBridgeOff.
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

// reopenOnMainIfLegacy manda por la línea nueva una plantilla destinada a una conversación de la línea
// vieja (sus plantillas no existen allí): abre una conversación en la principal al contacto de la
// vieja. handled=false = la conversación no es de la línea vieja (o el puente no reparte por línea) y
// quien llama sigue su camino de siempre. handled=true = ya está resuelto, con su id de mensaje o su
// error; ante un contacto ilegible o sin número no se envía nada.
//
// Lo usan SendTemplate y, a través de él, la rama `nectar:` de SendProactiveTemplate.
func (c *Client) reopenOnMainIfLegacy(ctx context.Context, convID int64, tmpl TemplateConfig) (handled bool, messageID string, err error) {
	lines, ok := c.nectar.(NectarLines)
	if !ok || !lines.IsLegacy(ctx, convID) {
		return false, "", nil
	}
	contact, err := c.nectar.Contact(ctx, convID)
	if err != nil {
		return true, "", fmt.Errorf("nectar: contacto de la conversación vieja %d: %w", convID, err)
	}
	// El identificador viene sin «+»; igual que la adopción de pendings por contacto.
	phone := "+" + contact.Identifier
	if contact.Identifier == "" || !utils.IsE164(phone) {
		return true, "", fmt.Errorf("nectar: la conversación vieja %d no publica un teléfono contactable: %w", convID, ErrNonContactable)
	}

	_, msgID, err := c.nectar.OpenWithTemplate(ctx, phone, tmpl)
	var already *AlreadyOpenError
	if errors.As(err, &already) {
		if !already.Mine {
			return true, "", fmt.Errorf("%w (conversation_id=%d)", ErrConversationWithAgent, already.ConversationID)
		}
		if already.ConversationID <= 0 {
			return true, "", fmt.Errorf("nectar: already_open sin conversation_id: %w", err)
		}
		msgID, err = c.nectar.SendTemplate(ctx, already.ConversationID, tmpl)
	}
	return true, msgID, err
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
