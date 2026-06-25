package local

import (
	"context"
	"database/sql"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/notifications"
)

// NotificationRepo handles persistence of pending notifications.
type NotificationRepo struct {
	db *sql.DB
}

func NewNotificationRepo(db *sql.DB) *NotificationRepo {
	return &NotificationRepo{db: db}
}

// Upsert inserts or updates a pending notification (keyed by phone).
// call_id is NOT updated here — use UpdateCallID for that.
func (r *NotificationRepo) Upsert(ctx context.Context, phone, nType, apptID, wlID, birdMsgID, convID string, retryCount int, expiresAt time.Time) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO notification_pending (phone, type, appointment_id, waiting_list_id, bird_message_id, conversation_id, retry_count, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON DUPLICATE KEY UPDATE
		   type = VALUES(type),
		   appointment_id = VALUES(appointment_id),
		   waiting_list_id = VALUES(waiting_list_id),
		   bird_message_id = VALUES(bird_message_id),
		   conversation_id = VALUES(conversation_id),
		   retry_count = VALUES(retry_count),
		   expires_at = VALUES(expires_at)`,
		phone, nType, apptID, wlID, birdMsgID, convID, retryCount, expiresAt)
	return err
}

// UpdateCallID persists the Bird IVR call ID to the pending notification row.
// This enables callIDMap reconstruction after a server restart.
func (r *NotificationRepo) UpdateCallID(ctx context.Context, phone, callID string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE notification_pending SET call_id = ? WHERE phone = ?`,
		callID, phone)
	return err
}

// Resolve mueve la notificación pendiente a notification_history con su estado final (confirmed,
// cancelled, rescheduled, expired, escalated_to_ivr, agent_resolved, ...) y la quita de la tabla
// activa. Conserva conversation_id/bird_message_id para tener evidencia (en qué chat, qué desenlace).
// Si no hay fila pendiente para ese phone, es no-op.
func (r *NotificationRepo) Resolve(ctx context.Context, phone, status string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO notification_history
		   (phone, type, appointment_id, waiting_list_id, bird_message_id, conversation_id, status, created_at, resolved_at)
		 SELECT phone, type, appointment_id, waiting_list_id, bird_message_id, conversation_id, ?, created_at, NOW()
		 FROM notification_pending WHERE phone = ?`, status, phone); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM notification_pending WHERE phone = ?`, phone); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteHistoryOlderThan borra el historial de notificaciones con más de `days` días (por
// resolved_at) para que la tabla no crezca indefinidamente. Devuelve cuántos borró.
func (r *NotificationRepo) DeleteHistoryOlderThan(ctx context.Context, days int) (int64, error) {
	res, err := r.db.ExecContext(ctx,
		`DELETE FROM notification_history WHERE resolved_at < (NOW() - INTERVAL ? DAY)`, days)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// FindExpired returns all pending notifications whose expires_at has passed.
func (r *NotificationRepo) FindExpired(ctx context.Context) ([]notifications.PendingRow, error) {
	return r.queryRows(ctx,
		`SELECT phone, type, COALESCE(appointment_id, ''), COALESCE(waiting_list_id, ''),
		        COALESCE(bird_message_id, ''), COALESCE(conversation_id, ''),
		        COALESCE(call_id, ''),
		        retry_count, expires_at, created_at
		 FROM notification_pending
		 WHERE expires_at < NOW()
		 ORDER BY expires_at ASC`)
}

// FindAll returns all pending notifications (used for restore on startup).
func (r *NotificationRepo) FindAll(ctx context.Context) ([]notifications.PendingRow, error) {
	return r.queryRows(ctx,
		`SELECT phone, type, COALESCE(appointment_id, ''), COALESCE(waiting_list_id, ''),
		        COALESCE(bird_message_id, ''), COALESCE(conversation_id, ''),
		        COALESCE(call_id, ''),
		        retry_count, expires_at, created_at
		 FROM notification_pending
		 ORDER BY created_at ASC`)
}

func (r *NotificationRepo) queryRows(ctx context.Context, query string) ([]notifications.PendingRow, error) {
	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []notifications.PendingRow
	for rows.Next() {
		var row notifications.PendingRow
		if err := rows.Scan(
			&row.Phone, &row.Type, &row.AppointmentID, &row.WaitingListID,
			&row.BirdMessageID, &row.ConversationID, &row.CallID,
			&row.RetryCount, &row.ExpiresAt, &row.CreatedAt,
		); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}
