package local

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// InboxRow represents a persisted inbound message for crash recovery.
type InboxRow struct {
	ID         string
	Phone      string
	RawBody    string
	MsgType    string
	ReceivedAt time.Time
}

// InboxRepo handles persistence of inbound messages (Write-Ahead Log).
type InboxRepo struct {
	db *sql.DB
}

func NewInboxRepo(db *sql.DB) *InboxRepo {
	return &InboxRepo{db: db}
}

// InsertIfNotExists persists an inbound message. Returns true if inserted (not a duplicate).
func (r *InboxRepo) InsertIfNotExists(ctx context.Context, id, phone, rawBody, msgType string, receivedAt time.Time) (bool, error) {
	// #14 (auditoría): ON DUPLICATE KEY UPDATE en vez de INSERT IGNORE — IGNORE traga TODOS los errores
	// (truncado, etc.) como si fueran duplicados; así solo se silencia el choque de PK. Fila nueva →
	// RowsAffected=1; duplicado (id=id, sin cambio) → 0; error real → se propaga.
	res, err := r.db.ExecContext(ctx,
		`INSERT INTO message_inbox (id, phone, raw_body, msg_type, received_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON DUPLICATE KEY UPDATE id = id`,
		id, phone, rawBody, msgType, receivedAt)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// MarkDone marks a message as processed.
func (r *InboxRepo) MarkDone(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE message_inbox SET status = 'done', processed_at = NOW() WHERE id = ?`, id)
	return err
}

// PoisonExhausted marca como 'poisoned' las filas 'pending' que ya agotaron sus replays de arranque
// y devuelve sus ids. Debe llamarse ANTES de FindPending: así una fila que mata al proceso deja de
// replayarse tras maxAttempts arranques, en vez de dejar al bot en bucle de reinicio permanente.
// Ver migración 035.
func (r *InboxRepo) PoisonExhausted(ctx context.Context, maxAttempts int) ([]string, error) {
	ids, err := r.pendingIDsWithAttemptsAtLeast(ctx, maxAttempts)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}

	if _, err := r.db.ExecContext(ctx,
		`UPDATE message_inbox SET status = 'poisoned', processed_at = NOW()
		 WHERE status = 'pending' AND attempts >= ?`, maxAttempts); err != nil {
		return nil, err
	}
	return ids, nil
}

// pendingIDsWithAttemptsAtLeast lee los ids a descartar antes del UPDATE, para poder reportarlos en
// la alerta (el UPDATE solo devuelve un conteo, y saber CUÁLES es lo que permite ir a buscarlos).
func (r *InboxRepo) pendingIDsWithAttemptsAtLeast(ctx context.Context, maxAttempts int) ([]string, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id FROM message_inbox WHERE status = 'pending' AND attempts >= ?`, maxAttempts)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// MarkReplayAttempt incrementa el contador de replays de las filas dadas. Se llama ANTES de encolar:
// si el mensaje mata al proceso, el intento ya quedó persistido y el siguiente arranque lo cuenta.
func (r *InboxRepo) MarkReplayAttempt(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	placeholders := strings.Repeat(",?", len(ids))[1:]
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	//nolint:gosec // G202: `placeholders` es una tira de "?" generada de len(ids), no input del
	// usuario; los ids viajan como parámetros. No hay forma de parametrizar un IN de largo variable.
	_, err := r.db.ExecContext(ctx,
		`UPDATE message_inbox SET attempts = attempts + 1 WHERE id IN (`+placeholders+`)`, args...)
	return err
}

// FindPending returns unprocessed messages ordered by received time, capped at limit (0 = sin tope).
// El tope acota la memoria: cada fila trae su raw_body completo, así que un backlog grande cargado
// de golpe puede costar cientos de MB dentro del proceso.
func (r *InboxRepo) FindPending(ctx context.Context, limit int) ([]InboxRow, error) {
	query := `SELECT id, phone, raw_body, msg_type, received_at
		 FROM message_inbox
		 WHERE status = 'pending'
		 ORDER BY received_at ASC`
	args := []interface{}{}
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []InboxRow
	for rows.Next() {
		var row InboxRow
		if err := rows.Scan(&row.ID, &row.Phone, &row.RawBody, &row.MsgType, &row.ReceivedAt); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

// FindPendingOlderThan returns unprocessed messages received more than `minutes` ago.
// El umbral evita reprocesar mensajes EN VUELO (que se completan en segundos): solo recupera los
// que quedaron realmente atascados en 'pending' (p.ej. descartados por backpressure, M7).
func (r *InboxRepo) FindPendingOlderThan(ctx context.Context, minutes int) ([]InboxRow, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, phone, raw_body, msg_type, received_at
		 FROM message_inbox
		 WHERE status = 'pending' AND received_at < DATE_SUB(NOW(), INTERVAL ? MINUTE)
		 ORDER BY received_at ASC`, minutes)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var result []InboxRow
	for rows.Next() {
		var row InboxRow
		if err := rows.Scan(&row.ID, &row.Phone, &row.RawBody, &row.MsgType, &row.ReceivedAt); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

// DeleteOlderThan removes processed messages older than the given number of hours.
func (r *InboxRepo) DeleteOlderThan(ctx context.Context, hours int) (int64, error) {
	res, err := r.db.ExecContext(ctx,
		`DELETE FROM message_inbox WHERE status = 'done' AND created_at < DATE_SUB(NOW(), INTERVAL ? HOUR)`,
		hours)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Count returns the number of rows by status (for metrics/debugging).
func (r *InboxRepo) Count(ctx context.Context, status string) (int64, error) {
	var count int64
	err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM message_inbox WHERE status = ?`, status).Scan(&count)
	return count, err
}
