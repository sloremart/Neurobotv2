package nectar

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/monitor"
)

// Aviso urgente de volver a Bird desde el cliente. Todas las rutas (las siete de siempre, la apertura
// y el horario) pasan por exchangeWithHeaders: es el único punto donde se ve un fallo de transporte o
// un 5xx de Néctar antes de que cada llamador lo traduzca a su manera. El escalado se cuenta aparte,
// en Client.Escalate, que es la única llamada de escalado del bot (Bridge.Escalate la envuelve).

// UrgentReporter es lo que el cliente necesita del aviso urgente (monitor.UrgentAlerter).
type UrgentReporter interface {
	Count(ctx context.Context, trigger monitor.UrgentTrigger, threshold int, window time.Duration, detail string)
}

// UrgentLimits son los umbrales del aviso: cuántos fallos en qué ventana (URGENT_NECTAR_DOWN_* y
// URGENT_ESCALATION_FAIL_*).
type UrgentLimits struct {
	DownCount            int
	DownWindow           time.Duration
	EscalationFailCount  int
	EscalationFailWindow time.Duration
}

// SetUrgentReporter cablea el aviso urgente. Se llama al arrancar, antes de cualquier petición
// (igual que bird.Client.SetNectar). Sin llamarlo, el cliente no avisa nada.
func (c *Client) SetUrgentReporter(r UrgentReporter, limits UrgentLimits) {
	c.urgent = r
	c.urgentLimits = limits
}

// statusError es un código de Néctar sin traducción propia (un 403 o 409 con otro motivo, otro
// status). msg es el texto de siempre (con el cuerpo, para el log); status y reason permiten al aviso
// urgente clasificarlo SIN copiar el cuerpo.
type statusError struct {
	status     int
	reason     string
	msg        string
	retryAfter time.Duration // la espera que pidió un 429; 0 si no pidió
}

func (e *statusError) Error() string { return e.msg }

// Unwrap marca el 429 como límite de la línea, igual que el de la apertura (decodeOpen): quien recorre
// una lista lo cuenta aparte, también si la plantilla salió por el already_open mine=true.
func (e *statusError) Unwrap() error {
	if e.status == http.StatusTooManyRequests {
		return bird.ErrLineRateLimited
	}
	return nil
}

// newStatusError arma el statusError de una respuesta: el motivo sale del cuerpo, el cuerpo no.
func newStatusError(resp reply, msg string) error {
	e := &statusError{status: resp.status, reason: replyReason(resp.body), msg: msg}
	if resp.status == http.StatusTooManyRequests {
		e.retryAfter = parseRetryAfter(resp.header.Get("Retry-After"))
	}
	return e
}

// maxParsedRetryAfter acota lo que se lee de la cabecera: más que suficiente para saber que es
// demasiado, y sin desbordar la duración con un número enorme.
const maxParsedRetryAfter = 24 * time.Hour

// parseRetryAfter lee la cabecera en cualquiera de sus dos formas (segundos o fecha). 0 si no viene,
// no se entiende o ya pasó.
func parseRetryAfter(h string) time.Duration {
	h = strings.TrimSpace(h)
	// Un número que no cabe en int llega con el extremo de su signo y ErrRange: sigue siendo «demasiado».
	if secs, err := strconv.Atoi(h); err == nil || errors.Is(err, strconv.ErrRange) {
		return max(0, time.Duration(min(secs, int(maxParsedRetryAfter/time.Second)))*time.Second)
	}
	if at, err := http.ParseTime(h); err == nil {
		return max(0, min(time.Until(at), maxParsedRetryAfter))
	}
	return 0
}

// downCountOffKey marca el contexto de un intento que no cuenta para TriggerNectarDown.
type downCountOffKey struct{}

// withoutDownCount es el contexto del último reintento de un envío: un mensaje fallido cuenta su envío
// y su primer reintento, y así el umbral sigue saltando con el mismo número de mensajes.
func withoutDownCount(ctx context.Context) context.Context {
	return context.WithValue(ctx, downCountOffKey{}, true)
}

// turnSendKey marca el contexto de un envío del turno (ver MarkTurnSend).
type turnSendKey struct{}

// MarkTurnSend marca el contexto de un envío que lleva el plazo del turno: si ese plazo vence, el bot lo
// abandonó sin darle a Néctar el tiempo del cliente, y no prueba que esté caída. Un plazo corto de
// cualquier otra llamada (la lectura de contacto de 5 s) sí cuenta.
func MarkTurnSend(ctx context.Context) context.Context {
	return context.WithValue(ctx, turnSendKey{}, true)
}

// abandonedByTurn dice si el fallo de transporte de esa petición es el abandono por el plazo del turno.
func abandonedByTurn(ctx context.Context) bool {
	return ctx.Err() != nil && ctx.Value(turnSendKey{}) != nil
}

// reportDown cuenta un fallo de Néctar (transporte o 5xx). class es solo la clase («timeout»,
// «HTTP 503»): ni cuerpos ni URLs viajan al aviso. El apagado del bot (contexto cancelado) no es
// Néctar caído; el timeout del cliente sí cuenta: Néctar no contestó a tiempo.
func (c *Client) reportDown(ctx context.Context, class string) {
	if c.urgent == nil || errors.Is(ctx.Err(), context.Canceled) || ctx.Value(downCountOffKey{}) != nil {
		return
	}
	c.urgent.Count(ctx, monitor.TriggerNectarDown, c.urgentLimits.DownCount, c.urgentLimits.DownWindow,
		"último: "+class)
}

// reportEscalation cuenta un escalado fallido que NO es caída de Néctar: el transporte y los 5xx ya
// cuentan en reportDown (un mismo corte no da dos avisos). Tampoco cuentan el 404 («ya no es mía»:
// otro tomó la conversación, respuesta de negocio) ni el apagado del bot.
func (c *Client) reportEscalation(ctx context.Context, err error) {
	if c.urgent == nil || err == nil || errors.Is(err, ErrNotMine) || errors.Is(ctx.Err(), context.Canceled) {
		return
	}
	class, ok := escalationFailureClass(err)
	if !ok {
		return
	}
	c.urgent.Count(ctx, monitor.TriggerEscalationFailing, c.urgentLimits.EscalationFailCount,
		c.urgentLimits.EscalationFailWindow, "último: "+class)
}

// escalationFailureClass da la clase de un escalado fallido para el aviso (código y motivo, nunca
// el cuerpo); ok=false si es transporte o 5xx, que ya cuentan como Néctar caído.
func escalationFailureClass(err error) (string, bool) {
	var transport *transportError
	var status *statusError
	switch {
	case errors.As(err, &transport):
		return "", false
	case errors.As(err, &status):
		if status.status >= 500 {
			return "", false
		}
		if status.reason != "" {
			return fmt.Sprintf("HTTP %d %s", status.status, status.reason), true
		}
		return fmt.Sprintf("HTTP %d", status.status), true
	case errors.Is(err, ErrEscalationDisabled):
		return "HTTP 403 escalation_disabled", true
	case errors.Is(err, ErrUnauthorized):
		return "HTTP 401", true
	case errors.Is(err, ErrNotConfiguredAPI):
		return "cliente sin configurar", true
	default:
		return "error sin clasificar", true
	}
}

// transportClass resume un fallo de transporte en su clase, sin la URL que lleva *url.Error. La
// conexión que no se pudo abrir se reconoce por el *net.OpError de "dial" (la misma regla que
// notSent), no por el errno: ECONNREFUSED no es el mismo código en Windows.
func transportClass(err error) string {
	var netErr net.Error
	var opErr *net.OpError
	switch {
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()):
		return "timeout"
	case errors.As(err, &opErr) && opErr.Op == "dial":
		return "conexión rechazada"
	default:
		return "transporte"
	}
}
