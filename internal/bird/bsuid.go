package bird

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/utils"
)

// Entrega a contactos whatsappusername/BSUID (privacidad de número de WhatsApp).
//
// VALIDADO EN VIVO contra Bird real (2026-08-12, workspace prod, paciente liso***io24):
//   - Conversations API a la conversación del username: el POST devuelve 201 pero la entrega
//     asíncrona FALLA SIEMPRE ("incorrect phone number format" — el puente de Bird usa el
//     username como si fuera teléfono). El bot creía haber respondido y el paciente no veía nada.
//   - Channels API con alias documentado `whatsappbsuid`: 422 contact not found (no opera aquí).
//   - Channels API con la key CRUDA del identifier del contacto (`whatsapp_<portfolioId>`) y el
//     BSUID (CO.xxx) como valor, POR EL CANAL DONDE EL PACIENTE ESCRIBIÓ: 202 → **delivered**.
//     (Por el canal de pruebas: sending_failed 15003 — el BSUID es scoped al número.)
//   - Resolución username→BSUID: PATCH /contacts/identifiers/whatsappusername/{username} con
//     body {} devuelve el contacto EXISTENTE (200) con todos sus identifiers, incluido el BSUID.
//
// Flujo: resolver contacto por el identificador recibido → extraer su identifier whatsapp_* →
// enviar por Channels con esa key/value. Sin BSUID en el contacto, el caller cae a la vía por
// conversación (visible en Inbox, aunque hoy no entregue — mejor rastro que nada).

// bsuidIdentifier busca en el contacto resuelto el identifier BSUID (key con prefijo
// "whatsapp_<portfolioId>"). Devuelve key y value, o "" si el contacto no lo tiene.
type contactIdentifiersResp struct {
	ID                  string `json:"id"`
	FeaturedIdentifiers []struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	} `json:"featuredIdentifiers"`
}

// resolveBsuid resuelve el identificador entrante (username o BSUID, tal como Bird lo entregó
// en el webhook bajo identifierKey=whatsappusername) al par (key, value) del BSUID del contacto.
// Usa PATCH upsert-por-identifier: si el contacto existe (siempre, acaba de escribir) devuelve
// su ficha completa sin modificar nada.
func (c *Client) resolveBsuid(ctx context.Context, identifier string) (key, value string, err error) {
	url := fmt.Sprintf("%s/workspaces/%s/contacts/identifiers/whatsappusername/%s",
		c.conversationsBase(), c.workspaceID, identifier)
	req, err := http.NewRequestWithContext(ctx, "PATCH", url, bytes.NewReader([]byte("{}")))
	if err != nil {
		return "", "", fmt.Errorf("resolve bsuid request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "AccessKey "+c.accessKeyID)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("resolve bsuid: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return "", "", fmt.Errorf("resolve bsuid: status %d", resp.StatusCode)
	}
	var contact contactIdentifiersResp
	if err := json.Unmarshal(body, &contact); err != nil {
		return "", "", fmt.Errorf("resolve bsuid: parse: %w", err)
	}
	for _, id := range contact.FeaturedIdentifiers {
		if strings.HasPrefix(id.Key, "whatsapp_") && id.Value != "" {
			return id.Key, id.Value, nil
		}
	}
	return "", "", fmt.Errorf("contact %s has no BSUID identifier", contact.ID)
}

// sendViaBsuid entrega un mensaje a un contacto username por la ÚNICA vía que llega de verdad
// (validada en vivo): Channels API direccionando al BSUID del contacto. No pasa por el gate
// E.164 de sendMessage (el BSUID no es un teléfono y este payload es correcto por diseño).
func (c *Client) sendViaBsuid(to string, body interface{}) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	bsuidKey, bsuidVal, err := c.resolveBsuid(ctx, to)
	if err != nil {
		// Visibilidad (H148): el caller degrada a ErrNonContactable, que enmascara la causa;
		// sin este WARN el diagnóstico de "por qué no salió" es a ciegas.
		slog.Warn("bsuid_resolve_failed", "phone", utils.MaskPhone(to), "error", err)
		return "", err
	}
	// H148: URL vía messagesURL() — BIRD_API_URL en prod trae la ruta COMPLETA del canal
	// (…/workspaces/{id}/channels/{id}); concatenarla a mano la DUPLICABA → 404 → ninguna
	// entrega BSUID funcionó en prod (16 'send message error' el 13-ago). messagesURL()
	// maneja ambos formatos (raíz y ruta completa).
	url := c.messagesURL()
	payload := map[string]interface{}{
		"receiver": map[string]interface{}{
			"contacts": []map[string]string{{
				"identifierKey":   bsuidKey,
				"identifierValue": bsuidVal,
			}},
		},
		"body": body,
	}
	id, err := c.sendMessage(url, payload) // sin arg phone → sin gate E.164 (intencional)
	if err != nil {
		slog.Warn("bsuid_send_failed", "phone", utils.MaskPhone(to), "bsuid_key", bsuidKey, "error", err)
		return "", fmt.Errorf("send via bsuid: %w", err)
	}
	slog.Info("delivered_via_bsuid",
		"phone", utils.MaskPhone(to), "bsuid_key", bsuidKey, "bird_msg_id", id)
	return id, nil
}
