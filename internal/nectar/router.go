package nectar

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/neuro-bot/neuro-bot/internal/bird"
)

// ErrTemplateOnLegacy es el rechazo de una plantilla sobre una conversación de la línea vieja:
// las plantillas (notificaciones) salen siempre por la línea nueva.
var ErrTemplateOnLegacy = errors.New("nectar: plantilla por la línea vieja: las notificaciones salen por la línea nueva")

// LineRouter reparte las llamadas por conversación entre el puente de cada línea de Néctar. Cada
// método con conversationID toma la línea del registro y delega en el puente de esa línea; la
// apertura con plantilla va siempre por la principal. Con solo la línea principal se comporta
// exactamente como su Bridge.
type LineRouter struct {
	reg     *LineRegistry
	bridges map[Line]*Bridge
}

var (
	_ bird.NectarBridge = (*LineRouter)(nil)
	_ bird.NectarLines  = (*LineRouter)(nil)
)

// NewLineRouter arma el router. Exige el puente de la línea principal (sin él no hay desvío).
func NewLineRouter(reg *LineRegistry, bridges map[Line]*Bridge) *LineRouter {
	if bridges[LineMain] == nil {
		panic("nectar: LineRouter sin puente de la línea principal")
	}
	return &LineRouter{reg: reg, bridges: bridges}
}

// IsLegacy dice si la conversación es de la línea vieja. Sin puente de línea vieja no hay nada que
// consultar: es siempre falso, sin tocar el registro.
func (r *LineRouter) IsLegacy(ctx context.Context, conversationID int64) bool {
	if r.bridges[LineLegacy] == nil {
		return false
	}
	return r.reg.Line(ctx, conversationID) == LineLegacy
}

// resolve devuelve la línea y el puente de la conversación. Sin puente de línea vieja todo es main y
// no se consulta el registro (el bot se comporta como sin línea vieja). Con ella, un fallo al leer el
// registro es un error: suponer main enviaría una conversación vieja con la clave principal. Registrada
// en una línea sin puente: no hay por dónde contestarle (ErrNectarBridgeOff).
func (r *LineRouter) resolve(ctx context.Context, conversationID int64) (Line, *Bridge, error) {
	if r.bridges[LineLegacy] == nil {
		return LineMain, r.bridges[LineMain], nil
	}
	line, err := r.reg.Lookup(ctx, conversationID)
	if err != nil {
		return "", nil, fmt.Errorf("nectar: no se pudo leer la línea de la conversación %d: %w", conversationID, err)
	}
	b := r.bridges[line]
	if b == nil {
		return line, nil, bird.ErrNectarBridgeOff
	}
	return line, b, nil
}

func (r *LineRouter) bridgeFor(ctx context.Context, conversationID int64) (*Bridge, error) {
	_, b, err := r.resolve(ctx, conversationID)
	return b, err
}

// SendText envía un texto por la línea de la conversación.
func (r *LineRouter) SendText(ctx context.Context, conversationID int64, text string) (string, error) {
	b, err := r.bridgeFor(ctx, conversationID)
	if err != nil {
		return "", err
	}
	return b.SendText(ctx, conversationID, text)
}

// SendButtons envía botones por la línea de la conversación.
func (r *LineRouter) SendButtons(ctx context.Context, conversationID int64, text string, buttons []bird.Button) (string, error) {
	b, err := r.bridgeFor(ctx, conversationID)
	if err != nil {
		return "", err
	}
	return b.SendButtons(ctx, conversationID, text, buttons)
}

// SendList envía una lista por la línea de la conversación.
func (r *LineRouter) SendList(ctx context.Context, conversationID int64, body, buttonLabel string, sections []bird.ListSection) (string, error) {
	b, err := r.bridgeFor(ctx, conversationID)
	if err != nil {
		return "", err
	}
	return b.SendList(ctx, conversationID, body, buttonLabel, sections)
}

// SendTemplate rechaza la línea vieja sin llamar a nadie: sus plantillas no existen allí.
func (r *LineRouter) SendTemplate(ctx context.Context, conversationID int64, tmpl bird.TemplateConfig) (string, error) {
	line, b, err := r.resolve(ctx, conversationID)
	if line == LineLegacy {
		return "", ErrTemplateOnLegacy
	}
	if err != nil {
		return "", err
	}
	return b.SendTemplate(ctx, conversationID, tmpl)
}

// Available pregunta la disponibilidad a la línea de la conversación.
func (r *LineRouter) Available(ctx context.Context, conversationID int64) (bool, string, error) {
	b, err := r.bridgeFor(ctx, conversationID)
	if err != nil {
		return false, "", err
	}
	return b.Available(ctx, conversationID)
}

// Escalate escala por la línea de la conversación.
func (r *LineRouter) Escalate(ctx context.Context, conversationID int64, esc bird.Escalation) error {
	b, err := r.bridgeFor(ctx, conversationID)
	if err != nil {
		return err
	}
	return b.Escalate(ctx, conversationID, esc)
}

// Close cierra por la línea de la conversación.
func (r *LineRouter) Close(ctx context.Context, conversationID int64) error {
	b, err := r.bridgeFor(ctx, conversationID)
	if err != nil {
		return err
	}
	return b.Close(ctx, conversationID)
}

// ConversationState lee el estado en la línea de la conversación.
func (r *LineRouter) ConversationState(ctx context.Context, conversationID int64) (string, error) {
	b, err := r.bridgeFor(ctx, conversationID)
	if err != nil {
		return "", err
	}
	return b.ConversationState(ctx, conversationID)
}

// Contact lee el contacto en la línea de la conversación.
func (r *LineRouter) Contact(ctx context.Context, conversationID int64) (bird.NectarContact, error) {
	b, err := r.bridgeFor(ctx, conversationID)
	if err != nil {
		return bird.NectarContact{}, err
	}
	return b.Contact(ctx, conversationID)
}

// WindowOpen lee la ventana de 24 h en la línea de la conversación.
func (r *LineRouter) WindowOpen(ctx context.Context, conversationID int64) (bool, error) {
	b, err := r.bridgeFor(ctx, conversationID)
	if err != nil {
		return false, err
	}
	return b.WindowOpen(ctx, conversationID)
}

// IsNotMine delega en la línea principal: el 404 «ya no es mía» es el mismo error en las dos.
func (r *LineRouter) IsNotMine(err error) bool { return r.bridges[LineMain].IsNotMine(err) }

// OpenWithTemplate abre SIEMPRE por la línea principal y registra la conversación abierta como
// principal. Si el registro falla solo se avisa: una conversación sin registro ya es principal.
func (r *LineRouter) OpenWithTemplate(ctx context.Context, phone string, tmpl bird.TemplateConfig) (int64, string, error) {
	id, msgID, err := r.bridges[LineMain].OpenWithTemplate(ctx, phone, tmpl)
	if err != nil {
		return id, msgID, err
	}
	if regErr := r.reg.Register(ctx, id, LineMain); regErr != nil {
		slog.Warn("nectar: no se pudo registrar la línea de la conversación abierta; se asume main",
			"conversation_id", id, "error", regErr)
	}
	return id, msgID, nil
}
