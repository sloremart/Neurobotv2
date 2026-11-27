package nectar

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strconv"

	"github.com/google/uuid"

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
	// ChannelID es el canal de Néctar por el que el bot ABRE conversaciones con plantilla
	// (NECTAR_CHANNEL_ID). 0 = la apertura no está configurada: OpenWithTemplate falla con
	// ErrOpenNotConfigured sin llamar a Néctar.
	ChannelID int64
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
	body, err := b.templateBody(tmpl)
	if err != nil {
		return "", err
	}
	id, err := b.c.SendTemplate(ctx, conversationID, body)
	return id64(id), err
}

// templateBody traduce la plantilla de Bird al cable de Néctar. La comparten el envío sobre una
// conversación viva y la apertura: un solo traductor, un solo idioma por defecto.
func (b *Bridge) templateBody(tmpl bird.TemplateConfig) (TemplateBody, error) {
	name, ok := b.TemplateNames[tmpl.ProjectID]
	if !ok || name == "" {
		return TemplateBody{}, fmt.Errorf("nectar: plantilla sin nombre de Meta configurado para project_id %q (NECTAR_TEMPLATE_NAMES)", tmpl.ProjectID)
	}
	params := make(map[string]string, len(tmpl.Params))
	for _, p := range tmpl.Params {
		params[p.Key] = p.Value
	}
	lang := tmpl.Locale
	if lang == "" {
		lang = "es"
	}
	return TemplateBody{Name: name, Language: lang, Parameters: params}, nil
}

// OpenWithTemplate abre una conversación nueva con una plantilla al teléfono dado, por el canal
// de apertura (ChannelID). Devuelve el id de la conversación y el del mensaje. El 409
// already_open sale como *AlreadyOpenError tal cual: decidir qué hacer con él es de quien llama.
func (b *Bridge) OpenWithTemplate(ctx context.Context, phone string, tmpl bird.TemplateConfig) (int64, string, error) {
	if b.ChannelID == 0 {
		return 0, "", ErrOpenNotConfigured
	}
	body, err := b.templateBody(tmpl)
	if err != nil {
		return 0, "", err
	}
	opened, err := b.c.OpenConversation(ctx, b.ChannelID, OpenRecipient{Phone: phone}, body, "")
	if err != nil {
		return 0, "", err
	}
	return opened.ConversationID, id64(opened.MessageID), nil
}

// Contact devuelve a quién le habla el bot en la conversación (bloque `contact` de GET …/state).
// Sin bloque, el contacto va vacío. No se persiste en crudo (Plan 44 §1.2).
func (b *Bridge) Contact(ctx context.Context, conversationID int64) (bird.NectarContact, error) {
	st, err := b.c.GetState(ctx, conversationID)
	if err != nil || st.Contact == nil {
		return bird.NectarContact{}, err
	}
	return bird.NectarContact{Identifier: st.Contact.Identifier, BSUID: st.Contact.BSUID}, nil
}

// WindowOpen dice si la ventana de 24 h de la conversación está abierta (GET …/state).
func (b *Bridge) WindowOpen(ctx context.Context, conversationID int64) (bool, error) {
	st, err := b.c.GetState(ctx, conversationID)
	return st.WindowOpen, err
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

// uploadKeyNamespace es el espacio fijo de las claves de subida (UUID v5).
var uploadKeyNamespace = uuid.MustParse("6b0f7a52-3c1e-4d7b-9a1f-5e2d8c4b7a10")

// uploadKey deriva la clave de subida del contenido: conversación, nombre y SHA-256 de los bytes. Así el
// reintento de un envío en duda sube con la MISMA clave, Néctar devuelve el mismo adjunto y el cuerpo del
// mensaje (attachment.id) conserva su huella, de modo que send reutiliza su clave de idempotencia.
func uploadKey(conversationID int64, filename string, data []byte) string {
	sum := sha256.Sum256(data)
	return uuid.NewSHA1(uploadKeyNamespace, []byte(id64(conversationID)+"|"+filename+"|"+string(sum[:]))).String()
}

// SendDocument sube el archivo con una clave derivada del contenido (uploadKey) y lo envía por la conversación; devuelve el id
// del mensaje en Néctar como cadena. El nombre del archivo y el contenido no se registran.
func (b *Bridge) SendDocument(ctx context.Context, conversationID int64, filename string, data []byte, caption string) (string, error) {
	att, err := b.c.Upload(ctx, uploadKey(conversationID, filename, data), filename, data)
	if err != nil {
		return "", err
	}
	id, err := b.c.SendAttachment(ctx, conversationID, att.ID, caption)
	return id64(id), err
}
