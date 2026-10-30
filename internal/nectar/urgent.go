package nectar

import (
	"context"
	"errors"
	"fmt"
	"net"
	"syscall"
	"time"

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
	status int
	reason string
	msg    string
}

func (e *statusError) Error() string { return e.msg }

// newStatusError arma el statusError de una respuesta: el motivo sale del cuerpo, el cuerpo no.
func newStatusError(status int, raw []byte, msg string) error {
	return &statusError{status: status, reason: replyReason(raw), msg: msg}
}

// reportDown cuenta un fallo de Néctar (transporte o 5xx). class es solo la clase («timeout»,
// «HTTP 503»): ni cuerpos ni URLs viajan al aviso. El apagado del bot (contexto cancelado) no es
// Néctar caído; un tiempo vencido sí cuenta: Néctar no contestó a tiempo.
func (c *Client) reportDown(ctx context.Context, class string) {
	if c.urgent == nil || errors.Is(ctx.Err(), context.Canceled) {
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

// transportClass resume un fallo de transporte en su clase, sin la URL que lleva *url.Error.
func transportClass(err error) string {
	var netErr net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()):
		return "timeout"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "conexión rechazada"
	default:
		return "transporte"
	}
}
