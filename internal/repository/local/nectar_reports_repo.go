package local

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/nectar"
)

// NectarReportsRepo guarda en la BD local los datos que la sincronización lee de Néctar para los
// informes de asesores: los tramos de espera, el roster de agentes y el estado de la propia
// sincronización (migración 046).
//
// Las columnas DATETIME van en UTC, sin zona. La conexión del bot usa loc=America/Bogota, así que
// aquí se escribe el texto UTC a mano y se lee el reloj de pared como UTC; si no, el driver las
// desplazaría cinco horas.
type NectarReportsRepo struct {
	db *sql.DB
}

// NewNectarReportsRepo crea el repositorio de informes de Néctar sobre la BD local del bot.
func NewNectarReportsRepo(db *sql.DB) *NectarReportsRepo {
	return &NectarReportsRepo{db: db}
}

// reportsBatch es cuántas filas o ids van en una sola sentencia.
const reportsBatch = 200

const utcLayout = "2006-01-02 15:04:05"

// utcText da el valor de un DATETIME en UTC; nil → NULL.
func utcText(t *time.Time) interface{} {
	if t == nil {
		return nil
	}
	return t.UTC().Format(utcLayout)
}

// wallUTC reinterpreta el reloj de pared que el driver leyó (en su loc) como UTC.
func wallUTC(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC)
}

func wallUTCPtr(n sql.NullTime) *time.Time {
	if !n.Valid {
		return nil
	}
	t := wallUTC(n.Time)
	return &t
}

func int64Ptr(n sql.NullInt64) *int64 {
	if !n.Valid {
		return nil
	}
	v := n.Int64
	return &v
}

// placeholders da "?,?,?" con n signos.
func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// UpsertCycles inserta los tramos o los actualiza si ya existen (mismo id de Néctar). Pisa todo lo
// que Néctar publica y NUNCA toca escalation_id: ese lo pone el emparejamiento con las atenciones.
func (r *NectarReportsRepo) UpsertCycles(ctx context.Context, cycles []nectar.WaitingCycle) error {
	for start := 0; start < len(cycles); start += reportsBatch {
		end := start + reportsBatch
		if end > len(cycles) {
			end = len(cycles)
		}
		chunk := cycles[start:end]
		rows := make([]string, 0, len(chunk))
		args := make([]interface{}, 0, len(chunk)*12)
		for _, c := range chunk {
			opened := c.OpenedAt
			rows = append(rows, "(?,?,?,?,?,?,?,?,?,?,?,?)")
			args = append(args, c.ID, c.ConversationID, c.PreviousCycleID, c.TeamID, c.AgentID,
				utcText(&opened), utcText(c.AssignedAt), utcText(c.FirstResponseAt), utcText(c.ClosedAt),
				c.CloseReason, c.Backfilled, c.ContactMessagesBeforeFirstResponse)
		}
		//nolint:gosec // G202: `rows` es una tira de "(?,...)" armada con len(chunk); los valores viajan como parámetros.
		_, err := r.db.ExecContext(ctx,
			`INSERT INTO nectar_waiting_cycles
			 (id, conversation_id, previous_cycle_id, team_id, agent_id, opened_at, assigned_at,
			  first_response_at, closed_at, close_reason, backfilled, contact_msgs_before_response)
			 VALUES `+strings.Join(rows, ",")+`
			 ON DUPLICATE KEY UPDATE
			  conversation_id = VALUES(conversation_id), previous_cycle_id = VALUES(previous_cycle_id),
			  team_id = VALUES(team_id), agent_id = VALUES(agent_id), opened_at = VALUES(opened_at),
			  assigned_at = VALUES(assigned_at), first_response_at = VALUES(first_response_at),
			  closed_at = VALUES(closed_at), close_reason = VALUES(close_reason),
			  backfilled = VALUES(backfilled),
			  contact_msgs_before_response = VALUES(contact_msgs_before_response)`, args...)
		if err != nil {
			return fmt.Errorf("upsert nectar cycles: %w", err)
		}
	}
	return nil
}

// OldestOpenCycleOpenedAt devuelve la apertura del tramo sin cerrar más viejo, o nil si no hay
// ninguno: desde ahí la sincronización vuelve a leer para ver cerrarse los tramos abiertos.
func (r *NectarReportsRepo) OldestOpenCycleOpenedAt(ctx context.Context) (*time.Time, error) {
	var t sql.NullTime
	if err := r.db.QueryRowContext(ctx,
		`SELECT MIN(opened_at) FROM nectar_waiting_cycles WHERE closed_at IS NULL`).Scan(&t); err != nil {
		return nil, fmt.Errorf("oldest open nectar cycle: %w", err)
	}
	return wallUTCPtr(t), nil
}

// CyclesForConversations devuelve todos los tramos guardados de esas conversaciones (por
// conversación y apertura) y, por id de tramo, la atención a la que ya están marcados (nil = ninguna).
func (r *NectarReportsRepo) CyclesForConversations(ctx context.Context, convIDs []int64) ([]nectar.WaitingCycle, map[int64]*int64, error) {
	var cycles []nectar.WaitingCycle
	escalations := map[int64]*int64{}
	for start := 0; start < len(convIDs); start += reportsBatch {
		end := start + reportsBatch
		if end > len(convIDs) {
			end = len(convIDs)
		}
		args := make([]interface{}, 0, end-start)
		for _, id := range convIDs[start:end] {
			args = append(args, id)
		}
		if err := r.queryCycles(ctx, args, &cycles, escalations); err != nil {
			return nil, nil, err
		}
	}
	return cycles, escalations, nil
}

// queryCycles lee los tramos de un lote de conversaciones y los agrega a cycles y escalations.
func (r *NectarReportsRepo) queryCycles(ctx context.Context, convArgs []interface{}, cycles *[]nectar.WaitingCycle, escalations map[int64]*int64) error {
	//nolint:gosec // G202: placeholders() es una tira de "?" de largo len(convArgs), no input del usuario.
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, conversation_id, previous_cycle_id, team_id, agent_id, opened_at, assigned_at,
		        first_response_at, closed_at, close_reason, backfilled, contact_msgs_before_response,
		        escalation_id
		 FROM nectar_waiting_cycles
		 WHERE conversation_id IN (`+placeholders(len(convArgs))+`)
		 ORDER BY conversation_id, opened_at, id`, convArgs...)
	if err != nil {
		return fmt.Errorf("nectar cycles for conversations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var c nectar.WaitingCycle
		var prev, team, agent, esc sql.NullInt64
		var opened time.Time
		var assigned, first, closed sql.NullTime
		var reason sql.NullString
		if err := rows.Scan(&c.ID, &c.ConversationID, &prev, &team, &agent, &opened, &assigned,
			&first, &closed, &reason, &c.Backfilled, &c.ContactMessagesBeforeFirstResponse, &esc); err != nil {
			return fmt.Errorf("scan nectar cycle: %w", err)
		}
		c.PreviousCycleID, c.TeamID, c.AgentID = int64Ptr(prev), int64Ptr(team), int64Ptr(agent)
		c.OpenedAt = wallUTC(opened)
		c.AssignedAt, c.FirstResponseAt, c.ClosedAt = wallUTCPtr(assigned), wallUTCPtr(first), wallUTCPtr(closed)
		if reason.Valid {
			s := reason.String
			c.CloseReason = &s
		}
		*cycles = append(*cycles, c)
		escalations[c.ID] = int64Ptr(esc)
	}
	return rows.Err()
}

// SetCycleEscalation marca los tramos como parte de la atención escalationID.
func (r *NectarReportsRepo) SetCycleEscalation(ctx context.Context, cycleIDs []int64, escalationID uint64) error {
	for start := 0; start < len(cycleIDs); start += reportsBatch {
		end := start + reportsBatch
		if end > len(cycleIDs) {
			end = len(cycleIDs)
		}
		args := []interface{}{escalationID}
		for _, id := range cycleIDs[start:end] {
			args = append(args, id)
		}
		//nolint:gosec // G202: placeholders() es una tira de "?" de largo len(args)-1, no input del usuario.
		if _, err := r.db.ExecContext(ctx,
			`UPDATE nectar_waiting_cycles SET escalation_id = ? WHERE id IN (`+placeholders(len(args)-1)+`)`,
			args...); err != nil {
			return fmt.Errorf("set nectar cycle escalation: %w", err)
		}
	}
	return nil
}

// ReplaceAgents guarda el roster (upsert por id). No borra a nadie: Néctar ya devuelve también a
// los inactivos, y un tramo viejo puede nombrarlos.
func (r *NectarReportsRepo) ReplaceAgents(ctx context.Context, agents []nectar.ReportAgent) error {
	for _, a := range agents {
		teams := a.Teams
		if teams == nil {
			teams = []nectar.ReportTeam{}
		}
		teamsJSON, err := json.Marshal(teams)
		if err != nil {
			return fmt.Errorf("marshal nectar agent teams: %w", err)
		}
		if _, err := r.db.ExecContext(ctx,
			`INSERT INTO nectar_agents (id, name, active, presence, teams, open_conversations)
			 VALUES (?, ?, ?, ?, ?, ?)
			 ON DUPLICATE KEY UPDATE name = VALUES(name), active = VALUES(active),
			  presence = VALUES(presence), teams = VALUES(teams),
			  open_conversations = VALUES(open_conversations)`,
			a.ID, a.Name, a.Active, a.Presence, string(teamsJSON), a.OpenConversations); err != nil {
			return fmt.Errorf("upsert nectar agent: %w", err)
		}
	}
	return nil
}

// GetState lee un valor del estado de la sincronización; vacío si no existe.
func (r *NectarReportsRepo) GetState(ctx context.Context, name string) (string, error) {
	var v string
	err := r.db.QueryRowContext(ctx, `SELECT value FROM nectar_sync_state WHERE name = ?`, name).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get nectar sync state: %w", err)
	}
	return v, nil
}

// SetState guarda un valor del estado de la sincronización.
func (r *NectarReportsRepo) SetState(ctx context.Context, name, value string) error {
	if _, err := r.db.ExecContext(ctx,
		`INSERT INTO nectar_sync_state (name, value) VALUES (?, ?)
		 ON DUPLICATE KEY UPDATE value = VALUES(value)`, name, value); err != nil {
		return fmt.Errorf("set nectar sync state: %w", err)
	}
	return nil
}
