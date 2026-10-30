package nectar

import (
	"context"
	"errors"
	"fmt"
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

// reportDown cuenta un fallo de Néctar (transporte o 5xx). El apagado del bot (contexto cancelado)
// no es Néctar caído; un tiempo vencido sí cuenta: Néctar no contestó a tiempo.
func (c *Client) reportDown(ctx context.Context, method, path, what string) {
	if c.urgent == nil || errors.Is(ctx.Err(), context.Canceled) {
		return
	}
	c.urgent.Count(ctx, monitor.TriggerNectarDown, c.urgentLimits.DownCount, c.urgentLimits.DownWindow,
		fmt.Sprintf("último: %s %s → %s", method, path, what))
}

// reportEscalation cuenta un escalado fallido. El 404 («ya no es mía»: otro tomó la conversación)
// es respuesta de negocio, no un escalado roto; el apagado del bot tampoco cuenta.
func (c *Client) reportEscalation(ctx context.Context, err error) {
	if c.urgent == nil || err == nil || errors.Is(err, ErrNotMine) || errors.Is(ctx.Err(), context.Canceled) {
		return
	}
	c.urgent.Count(ctx, monitor.TriggerEscalationFailing, c.urgentLimits.EscalationFailCount,
		c.urgentLimits.EscalationFailWindow, fmt.Sprintf("último error: %v", err))
}
