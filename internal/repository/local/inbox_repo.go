package local

import (
	"context"
	"database/sql"
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

// FindPending returns all unprocessed messages ordered by received time.
func (r *InboxRepo) FindPending(ctx context.Context) ([]InboxRow, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, phone, raw_body, msg_type, received_at
		 FROM message_inbox
		 WHERE status = 'pending'
		 ORDER BY received_at ASC`)
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
