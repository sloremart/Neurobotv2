package local

import (
	"context"
	"database/sql"
	"fmt"
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

// Create inserta una nueva escalación (al momento de escalar).
func (r *EscalationRepo) Create(ctx context.Context, sessionID, phone, fromState, teamID, agentID, agentName string) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO escalations (session_id, phone_number, from_state, team_id, agent_id, agent_name, escalated_at)
		 VALUES (?, ?, ?, ?, ?, ?, NOW())`,
		sessionID, phone, nullString(fromState), nullString(teamID), nullString(agentID), nullString(agentName))
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
