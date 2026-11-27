package local

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// EscalationRepo registra una fila por ESCALACIÓN (no por sesión): una conversación puede escalar
// varias veces. Captura el paso del chat (from_state), el agente asignado y las marcas de tiempo del
// ciclo (primera respuesta, devolución) + el desenlace (returned/completed/expired). Es la fuente de
// verdad del SLA por escalación y del KPI de "en qué paso se confunden los pacientes".
type EscalationRepo struct {
	db *sql.DB
}

// NewEscalationRepo crea el repositorio de escalaciones sobre la BD local del bot.
func NewEscalationRepo(db *sql.DB) *EscalationRepo {
	return &EscalationRepo{db: db}
}

// Create inserta una nueva escalación (al momento de escalar). El contacto de Néctar se copia de
// la sesión (sessions.contact_identifier, migración 045); en Bird queda NULL.
func (r *EscalationRepo) Create(ctx context.Context, sessionID, phone, fromState, teamID, agentID, agentName string) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO escalations (session_id, phone_number, from_state, team_id, agent_id, agent_name, escalated_at,
		 contact_identifier)
		 VALUES (?, ?, ?, ?, ?, ?, NOW(), (SELECT s.contact_identifier FROM sessions s WHERE s.id = ?))`,
		sessionID, phone, nullString(fromState), nullString(teamID), nullString(agentID), nullString(agentName),
		sessionID)
	if err != nil {
		return fmt.Errorf("create escalation: %w", err)
	}
	return nil
}

// TouchAgent sella la primera respuesta del agente (una vez) y la última actividad, en la escalación
// ABIERTA más reciente del teléfono.
func (r *EscalationRepo) TouchAgent(ctx context.Context, phone string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE escalations
		 SET first_agent_msg_at = COALESCE(first_agent_msg_at, NOW()), last_agent_msg_at = NOW()
		 WHERE phone_number = ? AND outcome IS NULL
		 ORDER BY escalated_at DESC LIMIT 1`, phone)
	if err != nil {
		return fmt.Errorf("escalation touch agent: %w", err)
	}
	return nil
}

// HasAgentResponded informa si el AGENTE ya respondió en la escalación ABIERTA más reciente del
// teléfono (first_agent_msg_at, sellado por TouchAgent). Solo lectura; lo usa el acuse de recibo
// durante escalación (§8.1 #6) para JAMÁS meterse en una conversación humana en curso.
func (r *EscalationRepo) HasAgentResponded(ctx context.Context, phone string) (bool, error) {
	var n int
	err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM (
			SELECT first_agent_msg_at FROM escalations
			WHERE phone_number = ? AND outcome IS NULL
			ORDER BY escalated_at DESC LIMIT 1
		) t WHERE t.first_agent_msg_at IS NOT NULL`, phone).Scan(&n)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// Resume marca la escalación abierta como devuelta al bot (resume).
func (r *EscalationRepo) Resume(ctx context.Context, phone string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE escalations SET resumed_at = NOW(), outcome = 'returned'
		 WHERE phone_number = ? AND outcome IS NULL
		 ORDER BY escalated_at DESC LIMIT 1`, phone)
	if err != nil {
		return fmt.Errorf("escalation resume: %w", err)
	}
	return nil
}

// Close marca la escalación abierta como cerrada por el agente (/bot cerrar).
func (r *EscalationRepo) Close(ctx context.Context, phone string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE escalations SET outcome = 'completed'
		 WHERE phone_number = ? AND outcome IS NULL
		 ORDER BY escalated_at DESC LIMIT 1`, phone)
	if err != nil {
		return fmt.Errorf("escalation close: %w", err)
	}
	return nil
}

// Expire marca la escalación abierta como expirada por silencio del paciente.
func (r *EscalationRepo) Expire(ctx context.Context, sessionID string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE escalations SET outcome = 'expired'
		 WHERE session_id = ? AND outcome IS NULL
		 ORDER BY escalated_at DESC LIMIT 1`, sessionID)
	if err != nil {
		return fmt.Errorf("escalation expire: %w", err)
	}
	return nil
}

// NoShow marca la escalación abierta como NO atendida por el agente: el agente nunca respondió
// (first_agent_msg_at IS NULL) tras agotar los recordatorios, así que el bot la devuelve al paciente.
// outcome='agent_no_show' es un terminal DISTINTO de 'expired' (abandono del paciente) y de 'returned'
// (devuelta por el agente): mide el incumplimiento de SLA del agente. resumed_at se sella porque la
// conversación vuelve al bot.
func (r *EscalationRepo) NoShow(ctx context.Context, sessionID string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE escalations SET outcome = 'agent_no_show', resumed_at = NOW()
		 WHERE session_id = ? AND outcome IS NULL
		 ORDER BY escalated_at DESC LIMIT 1`, sessionID)
	if err != nil {
		return fmt.Errorf("escalation no-show: %w", err)
	}
	return nil
}

// NectarEscalation es una atención escalada por Néctar (teléfono "nectar:<id>") tal como la lee la
// sincronización para emparejarla con los tramos de espera.
type NectarEscalation struct {
	ID              uint64
	SessionID       string
	Phone           string
	EscalatedAt     time.Time
	Outcome         *string
	FirstAgentMsgAt *time.Time
	AgentID         *string
}

// ListNectarEscalationsSince lista las escalaciones de Néctar desde since (de la más vieja a la nueva).
func (r *EscalationRepo) ListNectarEscalationsSince(ctx context.Context, since time.Time) ([]NectarEscalation, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, session_id, phone_number, escalated_at, outcome, first_agent_msg_at, agent_id
		 FROM escalations
		 WHERE phone_number LIKE 'nectar:%' AND escalated_at >= ?
		 ORDER BY escalated_at, id`, since)
	if err != nil {
		return nil, fmt.Errorf("list nectar escalations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []NectarEscalation
	for rows.Next() {
		var e NectarEscalation
		var outcome, agent sql.NullString
		var first sql.NullTime
		if err := rows.Scan(&e.ID, &e.SessionID, &e.Phone, &e.EscalatedAt, &outcome, &first, &agent); err != nil {
			return nil, fmt.Errorf("scan nectar escalation: %w", err)
		}
		if outcome.Valid {
			e.Outcome = &outcome.String
		}
		if first.Valid {
			e.FirstAgentMsgAt = &first.Time
		}
		if agent.Valid {
			e.AgentID = &agent.String
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// NectarAttentionUpdate es lo que la sincronización aprendió de Néctar sobre una atención.
type NectarAttentionUpdate struct {
	FirstResponseAt *time.Time
	AgentID         string
	AgentName       string
	ContactMsgs     *int
	NoShow          bool
}

// ApplyNectarAttention completa la atención con los datos de Néctar, solo de nulo a valor
// (COALESCE): nunca pisa lo ya guardado. Única excepción: agent_id y agent_name se pueden
// sobrescribir mientras first_agent_msg_at sea NULL (cuenta quien respondió); guardada la primera
// respuesta quedan fijos. NoShow cambia outcome de 'expired' a 'agent_no_show' y nada más: 'completed'
// es un agente que pulsó «Resolver» (igual que en Bird) y la cola vencida llega como devuelta.
//
// Orden de las asignaciones: MySQL las evalúa de izquierda a derecha, así que agent_id y
// agent_name van ANTES que first_agent_msg_at para ver si todavía era NULL.
func (r *EscalationRepo) ApplyNectarAttention(ctx context.Context, id uint64, a NectarAttentionUpdate) error {
	var first, msgs interface{}
	if a.FirstResponseAt != nil {
		first = *a.FirstResponseAt
	}
	if a.ContactMsgs != nil {
		msgs = *a.ContactMsgs
	}
	if _, err := r.db.ExecContext(ctx,
		`UPDATE escalations
		 SET agent_id = IF(first_agent_msg_at IS NULL, COALESCE(NULLIF(?, ''), agent_id), agent_id),
		     agent_name = IF(first_agent_msg_at IS NULL, COALESCE(NULLIF(?, ''), agent_name), agent_name),
		     first_agent_msg_at = COALESCE(first_agent_msg_at, ?),
		     last_agent_msg_at = COALESCE(last_agent_msg_at, ?),
		     contact_msgs_before_response = COALESCE(contact_msgs_before_response, ?),
		     outcome = IF(? AND outcome = 'expired', 'agent_no_show', outcome)
		 WHERE id = ?`,
		a.AgentID, a.AgentName, first, first, msgs, a.NoShow, id); err != nil {
		return fmt.Errorf("apply nectar attention: %w", err)
	}
	return nil
}

// SetResolution guarda quién resolvió y quién escaló en la escalación abierta más reciente del
// teléfono (mismo patrón que Close); vacío se guarda NULL.
func (r *EscalationRepo) SetResolution(ctx context.Context, phone, resolvedBy, escalatedBy string) error {
	if _, err := r.db.ExecContext(ctx,
		`UPDATE escalations SET resolved_by = ?, escalated_by = ?
		 WHERE phone_number = ? AND outcome IS NULL
		 ORDER BY escalated_at DESC LIMIT 1`,
		nullString(resolvedBy), nullString(escalatedBy), phone); err != nil {
		return fmt.Errorf("escalation set resolution: %w", err)
	}
	return nil
}
