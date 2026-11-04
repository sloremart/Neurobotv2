package nectar

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// LAS LECTURAS PARA LOS INFORMES DE ASESORES (Plan 45/46 de Néctar, enmiendas del 2026-10-30).
//
// Esta es la LECTURA y nada más: guardar los tramos, emparejarlos con las atenciones y mostrarlos
// es de la sincronización (D5). Las dos rutas rechazan la clave global (403 account_key_required):
// exigen la clave de CUENTA de la entidad.

// WaitingCyclesPath y AgentsPath son las rutas de las lecturas.
const (
	WaitingCyclesPath = "/api/bot/waiting-cycles"
	AgentsPath        = "/api/bot/agents"
)

// waitingCyclesLimit es el máximo que admite la ruta; se pide siempre para recorrer en pocas páginas.
const waitingCyclesLimit = 200

// ErrAccountKeyRequired es el 403 de estas rutas: se llamó con la clave global de transición, que
// no tiene workspace del cual leer. Es de configuración (falta la clave de cuenta), no de reintento.
var ErrAccountKeyRequired = errors.New("nectar: la ruta exige la clave de cuenta")

// WaitingCycle es un tramo de espera tal como lo publica Néctar. Los campos nulos del contrato son
// punteros: team_id nulo significa «desconocido» y close_reason nulo, tramo abierto (o anterior a
// la migración 144 de Néctar).
type WaitingCycle struct {
	ID                                 int64      `json:"id"`
	ConversationID                     int64      `json:"conversation_id"`
	PreviousCycleID                    *int64     `json:"previous_cycle_id"`
	TeamID                             *int64     `json:"team_id"`
	AgentID                            *int64     `json:"agent_id"`
	OpenedAt                           time.Time  `json:"opened_at"`
	AssignedAt                         *time.Time `json:"assigned_at"`
	FirstResponseAt                    *time.Time `json:"first_response_at"`
	ClosedAt                           *time.Time `json:"closed_at"`
	CloseReason                        *string    `json:"close_reason"`
	Backfilled                         bool       `json:"backfilled"`
	ContactMessagesBeforeFirstResponse int        `json:"contact_messages_before_first_response"`
}

// WaitingCyclesPage es una página de la lectura. NextCursor nulo = última página; HistorySince es
// desde cuándo vale el histórico (nulo si Néctar no tiene ninguno).
type WaitingCyclesPage struct {
	Cycles       []WaitingCycle `json:"cycles"`
	NextCursor   *string        `json:"next_cursor"`
	HistorySince *time.Time     `json:"history_since"`
}

// ReportTeam es un equipo del agente (incluye inactivos: un tramo viejo puede nombrarlos).
type ReportTeam struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// ReportAgent es un agente del roster de la entidad, activo o no.
type ReportAgent struct {
	ID                int64        `json:"id"`
	Name              string       `json:"name"`
	Active            bool         `json:"active"`
	Presence          string       `json:"presence"`
	Teams             []ReportTeam `json:"teams"`
	OpenConversations int          `json:"open_conversations"`
}

// GetWaitingCycles lee una página de GET /api/bot/waiting-cycles. since es obligatorio y until -
// since no puede pasar de 62 días (quien llama parte la lectura); cursor vacío = primera página.
// La firma cubre la URL completa (query incluida) y cada lectura lleva un `n` nuevo: repetir la
// misma URL en el mismo segundo da 401 replay. Sin reintentos: quien sincroniza repite la pasada.
func (c *Client) GetWaitingCycles(ctx context.Context, since, until time.Time, cursor string) (WaitingCyclesPage, error) {
	q := url.Values{}
	q.Set("since", since.UTC().Format(time.RFC3339))
	q.Set("until", until.UTC().Format(time.RFC3339))
	q.Set("limit", strconv.Itoa(waitingCyclesLimit))
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	q.Set("n", uuid.NewString())
	var out WaitingCyclesPage
	if err := c.getReport(ctx, WaitingCyclesPath, q, &out); err != nil {
		return WaitingCyclesPage{}, err
	}
	return out, nil
}

// GetAgents lee el roster completo de GET /api/bot/agents (sin paginar).
func (c *Client) GetAgents(ctx context.Context) ([]ReportAgent, error) {
	q := url.Values{}
	q.Set("n", uuid.NewString())
	var out struct {
		Agents []ReportAgent `json:"agents"`
	}
	if err := c.getReport(ctx, AgentsPath, q, &out); err != nil {
		return nil, err
	}
	return out.Agents, nil
}

// getReport hace un GET firmado y traduce los códigos comunes de las dos lecturas.
func (c *Client) getReport(ctx context.Context, path string, query url.Values, out any) error {
	resp, err := c.exchange(ctx, http.MethodGet, path+"?"+query.Encode(), nil)
	if err != nil {
		return err
	}
	switch {
	case resp.status == http.StatusUnauthorized:
		return ErrUnauthorized
	case resp.status == http.StatusForbidden && strings.Contains(string(resp.body), "account_key_required"):
		return ErrAccountKeyRequired
	case resp.status != http.StatusOK:
		return fmt.Errorf("nectar: GET %s: status %d %s", path, resp.status, bodySnippet(resp.body))
	}
	if err := json.Unmarshal(resp.body, out); err != nil {
		return fmt.Errorf("nectar: leer respuesta de %s: %w", path, err)
	}
	return nil
}
