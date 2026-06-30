package local

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/neuro-bot/neuro-bot/internal/session"
)

// mysqlErrDupEntry es el código de error de MySQL para violación de clave única (1062).
const mysqlErrDupEntry = 1062

type SessionRepo struct {
	db *sql.DB
}

func NewSessionRepo(db *sql.DB) *SessionRepo {
	return &SessionRepo{db: db}
}

// FindActiveByPhone busca una sesión activa o escalada para el teléfono dado.
// Incluye escalated para evitar crear sesiones duplicadas durante escalación.
func (r *SessionRepo) FindActiveByPhone(ctx context.Context, phone string) (*session.Session, error) {
	query := `SELECT id, phone_number, current_state, status, menu_option,
	          patient_id, patient_doc, patient_name, patient_age, patient_gender, patient_entity,
	          retry_count, conversation_id, escalated_at, escalated_team, agent_id, agent_name, resumed_at,
	          last_activity_at, expires_at, created_at
	          FROM sessions
	          WHERE phone_number = ? AND status IN ('active','escalated') AND expires_at > NOW()
	          ORDER BY last_activity_at DESC LIMIT 1`

	return r.scanSession(ctx, query, phone)
}

// scanSession scans a single session row from the given query.
func (r *SessionRepo) scanSession(ctx context.Context, query string, args ...interface{}) (*session.Session, error) {
	var s session.Session
	var menuOption, patientID, patientDoc, patientName, patientGender, patientEntity, conversationID sql.NullString
	var escalatedTeam, agentID, agentName sql.NullString
	var escalatedAt, resumedAt sql.NullTime
	var patientAge sql.NullInt32

	err := r.db.QueryRowContext(ctx, query, args...).Scan(
		&s.ID, &s.PhoneNumber, &s.CurrentState, &s.Status, &menuOption,
		&patientID, &patientDoc, &patientName, &patientAge, &patientGender, &patientEntity,
		&s.RetryCount, &conversationID, &escalatedAt, &escalatedTeam, &agentID, &agentName, &resumedAt,
		&s.LastActivity, &s.ExpiresAt, &s.CreatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scan session: %w", err)
	}

	s.MenuOption = menuOption.String
	s.PatientID = patientID.String
	s.PatientDoc = patientDoc.String
	s.PatientName = patientName.String
	if patientAge.Valid {
		s.PatientAge = int(patientAge.Int32)
	}
	s.PatientGender = patientGender.String
	s.PatientEntity = patientEntity.String
	s.ConversationID = conversationID.String
	s.EscalatedTeam = escalatedTeam.String
	s.AgentID = agentID.String
	s.AgentName = agentName.String
	if escalatedAt.Valid {
		s.EscalatedAt = &escalatedAt.Time
	}
	if resumedAt.Valid {
		s.ResumedAt = &resumedAt.Time
	}

	return &s, nil
}

// MarkEscalated sets the escalation tracking fields on a session.
func (r *SessionRepo) MarkEscalated(ctx context.Context, sessionID, teamID string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE sessions SET status = 'escalated', escalated_at = NOW(), escalated_team = ?,
		 last_activity_at = NOW(), updated_at = NOW() WHERE id = ?`,
		teamID, sessionID)
	if err != nil {
		return fmt.Errorf("mark escalated: %w", err)
	}
	return nil
}

// ResumeSession transitions a session from escalated back to active.
func (r *SessionRepo) ResumeSession(ctx context.Context, sessionID, newState string, timeoutMinutes int) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE sessions SET status = 'active', current_state = ?, resumed_at = NOW(),
		 last_activity_at = NOW(), expires_at = DATE_ADD(NOW(), INTERVAL ? MINUTE),
		 updated_at = NOW() WHERE id = ?`,
		newState, timeoutMinutes, sessionID)
	if err != nil {
		return fmt.Errorf("resume session: %w", err)
	}
	return nil
}

// Create inserta una nueva sesión
func (r *SessionRepo) Create(ctx context.Context, s *session.Session) error {
	query := `INSERT INTO sessions (id, phone_number, current_state, status, menu_option,
	          retry_count, last_activity_at, expires_at)
	          VALUES (?, ?, ?, ?, ?, ?, NOW(), ?)`

	_, err := r.db.ExecContext(ctx, query,
		s.ID, s.PhoneNumber, s.CurrentState, s.Status, nullString(s.MenuOption),
		s.RetryCount, s.ExpiresAt,
	)
	if err != nil {
		// M3: el índice único uq_active_phone (migración 032) rechazó una segunda sesión activa para el
		// mismo teléfono → sentinel para que FindOrCreate re-lea la ganadora en vez de duplicar.
		var myErr *mysql.MySQLError
		if errors.As(err, &myErr) && myErr.Number == mysqlErrDupEntry {
			return session.ErrActiveSessionExists
		}
		return fmt.Errorf("create session: %w", err)
	}
	return nil
}

// Save actualiza campos de la sesión (state, status, retry_count, etc.)
func (r *SessionRepo) Save(ctx context.Context, s *session.Session) error {
	query := `UPDATE sessions SET current_state = ?, status = ?, menu_option = ?,
	          patient_id = ?, patient_doc = ?, patient_name = ?,
	          patient_age = ?, patient_gender = ?, patient_entity = ?,
	          retry_count = ?, conversation_id = ?,
	          escalated_at = ?, escalated_team = ?, agent_id = ?, agent_name = ?, resumed_at = ?,
	          last_activity_at = NOW(), updated_at = NOW()
	          WHERE id = ?`

	_, err := r.db.ExecContext(ctx, query,
		s.CurrentState, s.Status, nullString(s.MenuOption),
		nullString(s.PatientID), nullString(s.PatientDoc), nullString(s.PatientName),
		nullInt(s.PatientAge), nullString(s.PatientGender), nullString(s.PatientEntity),
		s.RetryCount, nullString(s.ConversationID),
		s.EscalatedAt, nullString(s.EscalatedTeam), nullString(s.AgentID), nullString(s.AgentName), s.ResumedAt,
		s.ID,
	)
	if err != nil {
		return fmt.Errorf("save session: %w", err)
	}
	return nil
}

// UpdateStatus actualiza solo el status
func (r *SessionRepo) UpdateStatus(ctx context.Context, sessionID, status string) error {
	_, err := r.db.ExecContext(ctx,
		"UPDATE sessions SET status = ?, updated_at = NOW() WHERE id = ?",
		status, sessionID)
	if err != nil {
		return fmt.Errorf("update status: %w", err)
	}
	return nil
}

// CompleteActiveByPhone marks any active/escalated session for the given phone as completed.
// Used by the notification handler to close leftover sessions after confirm/cancel.
func (r *SessionRepo) CompleteActiveByPhone(ctx context.Context, phone string) error {
	_, err := r.db.ExecContext(ctx,
		"UPDATE sessions SET status = 'completed', updated_at = NOW() WHERE phone_number = ? AND status IN ('active','escalated') AND expires_at > NOW()",
		phone)
	if err != nil {
		return fmt.Errorf("complete active by phone: %w", err)
	}
	return nil
}

// UpdateConversationIDByPhone actualiza SOLO la columna conversation_id de la sesión activa/escalada
// del teléfono. Es un UPDATE de una columna (no reescribe la fila), así que es seguro sin el
// phone-lock y NO puede pisar current_state ni la PII del paciente — cierra el lost-update que tenía
// el webhook outbound al hacer FindActiveByPhone + Save de la fila completa (H2).
func (r *SessionRepo) UpdateConversationIDByPhone(ctx context.Context, phone, conversationID string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE sessions SET conversation_id = ?, updated_at = NOW()
		 WHERE phone_number = ? AND status IN ('active','escalated') AND expires_at > NOW()
		   AND (conversation_id IS NULL OR conversation_id <> ?)`,
		conversationID, phone, conversationID)
	if err != nil {
		return fmt.Errorf("update conversation_id by phone: %w", err)
	}
	return nil
}

// RenewExpiry renueva el expires_at
func (r *SessionRepo) RenewExpiry(ctx context.Context, sessionID string, expiresAt time.Time) error {
	_, err := r.db.ExecContext(ctx,
		"UPDATE sessions SET expires_at = ?, last_activity_at = NOW() WHERE id = ?",
		expiresAt, sessionID)
	if err != nil {
		return fmt.Errorf("renew expiry: %w", err)
	}
	return nil
}

// FindExpiredEscalatedSessions returns escalated sessions whose timeout has passed.
func (r *SessionRepo) FindExpiredEscalatedSessions(ctx context.Context) ([]session.ExpiredEscalatedSession, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, phone_number, COALESCE(conversation_id, '')
		 FROM sessions WHERE status = 'escalated' AND expires_at < NOW()`)
	if err != nil {
		return nil, fmt.Errorf("find expired escalated: %w", err)
	}
	defer rows.Close()

	var result []session.ExpiredEscalatedSession
	for rows.Next() {
		var s session.ExpiredEscalatedSession
		if err := rows.Scan(&s.ID, &s.PhoneNumber, &s.ConversationID); err != nil {
			return nil, fmt.Errorf("scan expired escalated: %w", err)
		}
		result = append(result, s)
	}
	return result, rows.Err()
}

// TouchPatientActivity registra un mensaje inbound del PACIENTE: renueva el TTL de la sesión y,
// crucialmente, sella last_patient_msg_at (reloj de cierre por silencio del paciente) y reinicia
// el contador de recordatorios (cada mensaje del paciente abre una nueva ventana de espera).
// NO la llama el flujo del agente — así el silencio del agente nunca reinicia el reloj de cierre.
func (r *SessionRepo) TouchPatientActivity(ctx context.Context, sessionID string, expiresAt time.Time) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE sessions SET last_patient_msg_at = NOW(), last_activity_at = NOW(),
		 expires_at = ?, agent_reminders_sent = 0, updated_at = NOW() WHERE id = ?`,
		expiresAt, sessionID)
	if err != nil {
		return fmt.Errorf("touch patient activity: %w", err)
	}
	return nil
}

// TouchAgentActivity sella last_agent_msg_at para la sesión escalada del teléfono dado.
// Se invoca cuando un saliente humano del agente llega por webhook; frena el recordatorio.
// No-op si no hay sesión escalada para ese teléfono.
func (r *SessionRepo) TouchAgentActivity(ctx context.Context, phone string) error {
	// first_agent_msg_at se sella una sola vez (COALESCE): es la PRIMERA respuesta del agente (TTFR).
	// last_agent_msg_at sí se actualiza en cada mensaje (frena el recordatorio).
	_, err := r.db.ExecContext(ctx,
		`UPDATE sessions SET last_agent_msg_at = NOW(),
		 first_agent_msg_at = COALESCE(first_agent_msg_at, NOW()), updated_at = NOW()
		 WHERE phone_number = ? AND status = 'escalated' AND expires_at > NOW()`,
		phone)
	if err != nil {
		return fmt.Errorf("touch agent activity: %w", err)
	}
	return nil
}

// FindEscalatedSessions returns all escalated sessions with the activity timestamps needed
// to decide close (patient silence) vs agent reminder.
func (r *SessionRepo) FindEscalatedSessions(ctx context.Context) ([]session.EscalatedSession, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, phone_number, COALESCE(conversation_id, ''),
		 COALESCE(last_patient_msg_at, last_activity_at), last_agent_msg_at, agent_reminders_sent
		 FROM sessions WHERE status = 'escalated'`)
	if err != nil {
		return nil, fmt.Errorf("find escalated sessions: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var result []session.EscalatedSession
	for rows.Next() {
		var s session.EscalatedSession
		var lastAgent sql.NullTime
		if err := rows.Scan(&s.ID, &s.PhoneNumber, &s.ConversationID,
			&s.LastPatientMsg, &lastAgent, &s.RemindersSent); err != nil {
			return nil, fmt.Errorf("scan escalated session: %w", err)
		}
		if lastAgent.Valid {
			s.LastAgentMsg = &lastAgent.Time
		}
		result = append(result, s)
	}
	return result, rows.Err()
}

// IncrementAgentReminders bumps the agent reminder counter for the current waiting window.
func (r *SessionRepo) IncrementAgentReminders(ctx context.Context, sessionID string) error {
	_, err := r.db.ExecContext(ctx,
		"UPDATE sessions SET agent_reminders_sent = agent_reminders_sent + 1, updated_at = NOW() WHERE id = ?",
		sessionID)
	if err != nil {
		return fmt.Errorf("increment agent reminders: %w", err)
	}
	return nil
}

// MarkAbandoned sets a session status to abandoned.
func (r *SessionRepo) MarkAbandoned(ctx context.Context, sessionID string) error {
	_, err := r.db.ExecContext(ctx,
		"UPDATE sessions SET status = 'abandoned', updated_at = NOW() WHERE id = ?", sessionID)
	if err != nil {
		return fmt.Errorf("mark abandoned: %w", err)
	}
	return nil
}

// FindInactiveSessions returns active sessions idle for at least idleMinutes,
// along with how many inactivity reminders have been sent.
func (r *SessionRepo) FindInactiveSessions(ctx context.Context, idleMinutes int) ([]session.InactiveSession, error) {
	query := `SELECT s.id, s.phone_number, COALESCE(s.conversation_id, ''),
	          s.last_activity_at, COALESCE(sc.ctx_value, '0')
	          FROM sessions s
	          LEFT JOIN session_context sc ON sc.session_id = s.id AND sc.ctx_key = 'inactivity_reminders'
	          WHERE s.status = 'active'
	          AND s.last_activity_at < DATE_SUB(NOW(), INTERVAL ? MINUTE)
	          ORDER BY s.last_activity_at ASC`

	rows, err := r.db.QueryContext(ctx, query, idleMinutes)
	if err != nil {
		return nil, fmt.Errorf("find inactive sessions: %w", err)
	}
	defer rows.Close()

	var result []session.InactiveSession
	for rows.Next() {
		var s session.InactiveSession
		var remStr string
		if err := rows.Scan(&s.ID, &s.PhoneNumber, &s.ConversationID, &s.LastActivity, &remStr); err != nil {
			return nil, fmt.Errorf("scan inactive session: %w", err)
		}
		fmt.Sscanf(remStr, "%d", &s.Reminders)
		result = append(result, s)
	}
	return result, rows.Err()
}

// --- Context operations ---

// SetContext guarda un par clave-valor en session_context (UPSERT)
func (r *SessionRepo) SetContext(ctx context.Context, sessionID, key, value string) error {
	query := `INSERT INTO session_context (session_id, ctx_key, ctx_value)
	          VALUES (?, ?, ?)
	          ON DUPLICATE KEY UPDATE ctx_value = VALUES(ctx_value), updated_at = NOW()`

	_, err := r.db.ExecContext(ctx, query, sessionID, key, value)
	if err != nil {
		return fmt.Errorf("set context %s: %w", key, err)
	}
	return nil
}

// SetContextBatch guarda múltiples pares clave-valor en una transacción
func (r *SessionRepo) SetContextBatch(ctx context.Context, sessionID string, kvs map[string]string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `INSERT INTO session_context (session_id, ctx_key, ctx_value)
	    VALUES (?, ?, ?)
	    ON DUPLICATE KEY UPDATE ctx_value = VALUES(ctx_value), updated_at = NOW()`)
	if err != nil {
		return fmt.Errorf("prepare: %w", err)
	}
	defer stmt.Close()

	for k, v := range kvs {
		if _, err := stmt.ExecContext(ctx, sessionID, k, v); err != nil {
			return fmt.Errorf("set context batch %s: %w", k, err)
		}
	}

	return tx.Commit()
}

// GetContext obtiene un valor del contexto
func (r *SessionRepo) GetContext(ctx context.Context, sessionID, key string) (string, error) {
	var value string
	err := r.db.QueryRowContext(ctx,
		"SELECT ctx_value FROM session_context WHERE session_id = ? AND ctx_key = ?",
		sessionID, key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get context %s: %w", key, err)
	}
	return value, nil
}

// GetAllContext obtiene todo el contexto como map
func (r *SessionRepo) GetAllContext(ctx context.Context, sessionID string) (map[string]string, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT ctx_key, ctx_value FROM session_context WHERE session_id = ?", sessionID)
	if err != nil {
		return nil, fmt.Errorf("get all context: %w", err)
	}
	defer rows.Close()

	result := make(map[string]string)
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, fmt.Errorf("scan context: %w", err)
		}
		result[key] = value
	}
	return result, rows.Err()
}

// ClearContext borra claves específicas del contexto
func (r *SessionRepo) ClearContext(ctx context.Context, sessionID string, keys ...string) error {
	if len(keys) == 0 {
		return nil
	}

	placeholders := make([]string, len(keys))
	args := make([]interface{}, 0, len(keys)+1)
	args = append(args, sessionID)
	for i, k := range keys {
		placeholders[i] = "?"
		args = append(args, k)
	}

	query := fmt.Sprintf("DELETE FROM session_context WHERE session_id = ? AND ctx_key IN (%s)",
		strings.Join(placeholders, ","))

	_, err := r.db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("clear context: %w", err)
	}
	return nil
}

// ClearAllContext borra todo el contexto de una sesión
func (r *SessionRepo) ClearAllContext(ctx context.Context, sessionID string) error {
	_, err := r.db.ExecContext(ctx,
		"DELETE FROM session_context WHERE session_id = ?", sessionID)
	if err != nil {
		return fmt.Errorf("clear all context: %w", err)
	}
	return nil
}

// FindByID returns a single session by its UUID.
func (r *SessionRepo) FindByID(ctx context.Context, sessionID string) (*session.Session, error) {
	query := `SELECT id, phone_number, current_state, status, menu_option,
	          patient_id, patient_doc, patient_name, patient_age, patient_gender, patient_entity,
	          retry_count, conversation_id, escalated_at, escalated_team, agent_id, agent_name, resumed_at,
	          last_activity_at, expires_at, created_at
	          FROM sessions WHERE id = ?`
	return r.scanSession(ctx, query, sessionID)
}

// FindRecentByPhone returns the last N sessions for a phone, any status.
func (r *SessionRepo) FindRecentByPhone(ctx context.Context, phone string, limit int) ([]session.Session, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, phone_number, current_state, status, menu_option,
		 patient_id, patient_doc, patient_name, patient_age, patient_gender, patient_entity,
		 retry_count, conversation_id, escalated_at, escalated_team, agent_id, agent_name, resumed_at,
		 last_activity_at, expires_at, created_at
		 FROM sessions WHERE phone_number = ? ORDER BY created_at DESC LIMIT ?`, phone, limit)
	if err != nil {
		return nil, fmt.Errorf("find recent by phone: %w", err)
	}
	defer rows.Close()

	var result []session.Session
	for rows.Next() {
		var s session.Session
		var menuOption, patientID, patientDoc, patientName, patientGender, patientEntity, conversationID sql.NullString
		var escalatedTeam, agentID, agentName sql.NullString
		var escalatedAt, resumedAt sql.NullTime
		var patientAge sql.NullInt32

		if err := rows.Scan(
			&s.ID, &s.PhoneNumber, &s.CurrentState, &s.Status, &menuOption,
			&patientID, &patientDoc, &patientName, &patientAge, &patientGender, &patientEntity,
			&s.RetryCount, &conversationID, &escalatedAt, &escalatedTeam, &agentID, &agentName, &resumedAt,
			&s.LastActivity, &s.ExpiresAt, &s.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan session row: %w", err)
		}

		s.MenuOption = menuOption.String
		s.PatientID = patientID.String
		s.PatientDoc = patientDoc.String
		s.PatientName = patientName.String
		if patientAge.Valid {
			s.PatientAge = int(patientAge.Int32)
		}
		s.PatientGender = patientGender.String
		s.PatientEntity = patientEntity.String
		s.ConversationID = conversationID.String
		s.EscalatedTeam = escalatedTeam.String
		s.AgentID = agentID.String
		s.AgentName = agentName.String
		if escalatedAt.Valid {
			s.EscalatedAt = &escalatedAt.Time
		}
		if resumedAt.Valid {
			s.ResumedAt = &resumedAt.Time
		}
		result = append(result, s)
	}
	return result, rows.Err()
}

// --- Helpers ---

func nullString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

func nullInt(i int) sql.NullInt32 {
	if i == 0 {
		return sql.NullInt32{}
	}
	return sql.NullInt32{Int32: int32(i), Valid: true}
}
