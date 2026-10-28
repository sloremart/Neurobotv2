package nectar

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/neuro-bot/neuro-bot/internal/bird"
)

// Bridge adapta Client a bird.NectarBridge: traduce los tipos de Bird (Button{Text,
// Payload}, ListSection, TemplateConfig) al cable de Néctar (§8.5) y devuelve los ids como
// cadena, que es lo que el cliente de Bird entrega a sus llamadores.
type Bridge struct {
	c *Client
	// TemplateNames es la correspondencia ProjectID de Bird → nombre de plantilla de Meta.
	// Vive en el bot, no en Néctar (§8.5: «Néctar no aprende un identificador de proyecto de
	// Bird»). Vacía → las plantillas fallan con un error que dice qué falta.
	TemplateNames map[string]string
}

// NewBridge arma el puente sobre un Client. templateNames traduce el ProjectID de Bird al nombre de
// plantilla de Meta (ver Bridge.TemplateNames).
func NewBridge(c *Client, templateNames map[string]string) *Bridge {
	return &Bridge{c: c, TemplateNames: templateNames}
}

var _ bird.NectarBridge = (*Bridge)(nil)

func id64(id int64) string { return strconv.FormatInt(id, 10) }

// SendText envía un texto por la conversación y devuelve el id del mensaje en Néctar como cadena.
func (b *Bridge) SendText(ctx context.Context, conversationID int64, text string) (string, error) {
	id, err := b.c.SendText(ctx, conversationID, text)
	return id64(id), err
}

// SendButtons envía botones de respuesta: Payload es id y Text es title (§8.5, tabla del envío).
func (b *Bridge) SendButtons(ctx context.Context, conversationID int64, text string, buttons []bird.Button) (string, error) {
	out := make([]ReplyButton, len(buttons))
	for i, btn := range buttons {
		out[i] = ReplyButton{ID: btn.Payload, Title: btn.Text}
	}
	id, err := b.c.SendButtons(ctx, conversationID, text, out)
	return id64(id), err
}

// SendList envía una lista interactiva: cada fila lleva id, título y descripción opcional.
func (b *Bridge) SendList(ctx context.Context, conversationID int64, body, buttonLabel string, sections []bird.ListSection) (string, error) {
	out := make([]ListSection, len(sections))
	for i, s := range sections {
		rows := make([]ListRow, len(s.Rows))
		for j, r := range s.Rows {
			rows[j] = ListRow{ID: r.ID, Title: r.Title, Description: r.Description}
		}
		out[i] = ListSection{Title: s.Title, Rows: rows}
	}
	id, err := b.c.SendList(ctx, conversationID, body, buttonLabel, out)
	return id64(id), err
}

// SendTemplate envía una plantilla por NOMBRE e IDIOMA de Meta y parámetros POR NOMBRE DE
// MARCADOR (objeto, no arreglo). TemplateParam.Key ya es el nombre del marcador.
func (b *Bridge) SendTemplate(ctx context.Context, conversationID int64, tmpl bird.TemplateConfig) (string, error) {
	name, ok := b.TemplateNames[tmpl.ProjectID]
	if !ok || name == "" {
		return "", fmt.Errorf("nectar: plantilla sin nombre de Meta configurado para project_id %q (NECTAR_TEMPLATE_NAMES)", tmpl.ProjectID)
	}
	params := make(map[string]string, len(tmpl.Params))
	for _, p := range tmpl.Params {
		params[p.Key] = p.Value
	}
	lang := tmpl.Locale
	if lang == "" {
		lang = "es"
	}
	id, err := b.c.SendTemplate(ctx, conversationID, TemplateBody{Name: name, Language: lang, Parameters: params})
	return id64(id), err
}

// Available dice si la conversación se puede escalar; «no» es una respuesta normal, no un error.
func (b *Bridge) Available(ctx context.Context, conversationID int64) (bool, string, error) {
	a, err := b.c.GetAvailability(ctx, conversationID)
	return a.Available, a.Reason, err
}

// Escalate pasa la conversación a un agente con el sobre entero; el equipo sugerido viaja en él.
func (b *Bridge) Escalate(ctx context.Context, conversationID int64, esc bird.Escalation) error {
	_, err := b.c.Escalate(ctx, conversationID, esc, esc.SuggestedTeamID)
	return err
}

// Close cierra la conversación por inactividad del bot.
func (b *Bridge) Close(ctx context.Context, conversationID int64) error {
	return b.c.Close(ctx, conversationID)
}

// ConversationState entrega el estado CRUDO de GET …/state. El puente no lo interpreta: decidir
// qué significa `waiting` o `assigned` es del bot (convención «el transporte no hace negocio»).
func (b *Bridge) ConversationState(ctx context.Context, conversationID int64) (string, error) {
	st, err := b.c.GetState(ctx, conversationID)
	return st.State, err
}

// IsNotMine reconoce el 404 «ya no es mía» (ErrNotMine): se suelta el caso, sin reintentar.
func (b *Bridge) IsNotMine(err error) bool { return errors.Is(err, ErrNotMine) }
