// Package bird implementa el cliente de la plataforma Bird (WhatsApp, Inbox, voz).
package bird

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// CallRecord es una PIERNA de llamada del canal de voz de Bird (una llamada puenteada agente↔paciente
// tiene 2 piernas unidas por ParentID). La pierna del AGENTE es la que lleva "client:<uuid>" en
// from (el agente llamó) o to (la llamada se le ofreció); el uuid ES el agent_id del inbox.
type CallRecord struct {
	ID        string `json:"id"`
	From      string `json:"from"`
	To        string `json:"to"`
	Direction string `json:"direction"`
	Status    string `json:"status"` // completed | no-answer | cancelled | ongoing | ringing
	Duration  int    `json:"duration"`
	CreatedAt string `json:"createdAt"` // RFC3339 (UTC)
	ParentID  string `json:"parentId"`
}

// CallListPage es una página del listado de llamadas.
type CallListPage struct {
	NextPageToken string       `json:"nextPageToken"`
	Results       []CallRecord `json:"results"`
}

// ListCalls lista las llamadas del canal de voz (más recientes primero), paginadas. Solo lectura;
// para las métricas de llamadas por agente del informe (F3).
func (c *Client) ListCalls(pageToken string) (*CallListPage, error) {
	if c.voiceChannelID == "" {
		return nil, fmt.Errorf("voice channel not configured")
	}
	reqURL := fmt.Sprintf("%s/workspaces/%s/channels/%s/calls?limit=100",
		c.conversationsBase(), c.workspaceID, c.voiceChannelID)
	if pageToken != "" {
		reqURL += "&pageToken=" + url.QueryEscape(pageToken)
	}
	req, err := http.NewRequest("GET", reqURL, nil) //nolint:noctx // cliente Bird sin ctx (mismo patron que listAgents)
	if err != nil {
		return nil, fmt.Errorf("create calls request: %w", err)
	}
	req.Header.Set("Authorization", "AccessKey "+c.accessKeyID)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list calls: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("list calls: status %d, body: %s", resp.StatusCode, string(body[:min(len(body), 300)]))
	}
	var page CallListPage
	if err := json.Unmarshal(body, &page); err != nil {
		return nil, fmt.Errorf("parse calls response: %w", err)
	}
	return &page, nil
}
