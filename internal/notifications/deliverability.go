package notifications

import (
	"context"
	"errors"
	"log/slog"

	"github.com/neuro-bot/neuro-bot/internal/utils"
)

// DeliverabilityChecker consulta los fallos de entrega de WhatsApp confirmados por el webhook
// de Bird (tabla message_delivery_failures).
type DeliverabilityChecker interface {
	ConsecutiveFailures(ctx context.Context, phone string) (int, error)
}

// deliveryFailureThreshold: fallos CONSECUTIVOS de entrega antes de suprimir templates
// programados. Con 2 se elimina el falso positivo de un fallo puntual de red/WA.
const deliveryFailureThreshold = 2

// ErrSuppressedNoWhatsApp señala que el número acumula fallos de entrega confirmados — enviarle
// otro template es un cobro garantizado sin entrega. Se trata como fallo PERMANENTE.
var ErrSuppressedNoWhatsApp = errors.New("suppressed: whatsapp delivery keeps failing")

// SetDeliverability inyecta el checker de fallos de entrega (nil = sin supresión, fail-open).
func (m *NotificationManager) SetDeliverability(d DeliverabilityChecker) {
	m.deliverability = d
}

// Deliverable reporta si vale la pena enviar templates programados al teléfono. false SOLO con
// >=2 fallos de entrega consecutivos confirmados por el webhook; ante checker ausente o error de
// BD es fail-open (un fallo de infraestructura no debe silenciar recordatorios).
func (m *NotificationManager) Deliverable(ctx context.Context, phone string) bool {
	if m.deliverability == nil {
		return true
	}
	n, err := m.deliverability.ConsecutiveFailures(ctx, phone)
	if err != nil {
		slog.Warn("deliverability check failed (fail-open)", "phone", utils.MaskPhone(phone), "error", err)
		return true
	}
	return n < deliveryFailureThreshold
}
