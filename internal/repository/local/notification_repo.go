package local

import (
	"context"
	"database/sql"
	"errors"
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
// call_id is NOT updated here — use UpdateCallID for that. contactPhone vacío (Bird) se guarda NULL.
// unsent marca el pending de un recordatorio que no salió (migración 045). apptSlot es la fecha y
// hora de la cita recordada (migración 047; vacía se guarda NULL).
func (r *NotificationRepo) Upsert(ctx context.Context, phone, nType, apptID, apptSlot, wlID, birdMsgID, convID, contactPhone string, unsent bool, retryCount int, expiresAt time.Time) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO notification_pending (phone, type, appointment_id, appointment_slot, waiting_list_id, bird_message_id, conversation_id, contact_phone, unsent, retry_count, expires_at)
		 VALUES (?, ?, ?, NULLIF(?, ''), ?, ?, ?, NULLIF(?, ''), ?, ?, ?)
		 ON DUPLICATE KEY UPDATE
		   type = VALUES(type),
		   appointment_id = VALUES(appointment_id),
		   appointment_slot = VALUES(appointment_slot),
		   waiting_list_id = VALUES(waiting_list_id),
		   bird_message_id = VALUES(bird_message_id),
		   conversation_id = VALUES(conversation_id),
		   contact_phone = VALUES(contact_phone),
		   unsent = VALUES(unsent),
		   retry_count = VALUES(retry_count),
		   expires_at = VALUES(expires_at)`,
		phone, nType, apptID, apptSlot, wlID, birdMsgID, convID, contactPhone, unsent, retryCount, expiresAt)
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

// Rekey pasa la fila de oldPhone a la clave newPhone con su conversación y su contacto (vacío se
// guarda NULL): la adopción del pending de un recordatorio fallido por una conversación de Néctar.
// expires_at se reasigna a sí mismo para que ningún ON UPDATE CURRENT_TIMESTAMP lo mueva. Si no hay
// fila para oldPhone, es no-op.
func (r *NotificationRepo) Rekey(ctx context.Context, oldPhone, newPhone, convID, contactPhone string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE notification_pending
		 SET phone = ?, conversation_id = ?, contact_phone = NULLIF(?, ''), expires_at = expires_at
		 WHERE phone = ?`,
		newPhone, convID, contactPhone, oldPhone)
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
		   (phone, type, appointment_id, appointment_slot, waiting_list_id, bird_message_id, conversation_id, contact_phone, unsent, status, created_at, resolved_at)
		 SELECT phone, type, appointment_id, appointment_slot, waiting_list_id, bird_message_id, conversation_id, contact_phone, unsent, ?, created_at, NOW()
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
		`SELECT phone, type, COALESCE(appointment_id, ''), COALESCE(appointment_slot, ''), COALESCE(waiting_list_id, ''),
		        COALESCE(bird_message_id, ''), COALESCE(conversation_id, ''),
		        COALESCE(contact_phone, ''), unsent, COALESCE(call_id, ''),
		        retry_count, expires_at, created_at
		 FROM notification_pending
		 WHERE expires_at < NOW()
		 ORDER BY expires_at ASC`)
}

// FindAll returns all pending notifications (used for restore on startup).
func (r *NotificationRepo) FindAll(ctx context.Context) ([]notifications.PendingRow, error) {
	return r.queryRows(ctx,
		`SELECT phone, type, COALESCE(appointment_id, ''), COALESCE(appointment_slot, ''), COALESCE(waiting_list_id, ''),
		        COALESCE(bird_message_id, ''), COALESCE(conversation_id, ''),
		        COALESCE(contact_phone, ''), unsent, COALESCE(call_id, ''),
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
			&row.Phone, &row.Type, &row.AppointmentID, &row.AppointmentSlot, &row.WaitingListID,
			&row.BirdMessageID, &row.ConversationID, &row.ContactPhone, &row.Unsent, &row.CallID,
			&row.RetryCount, &row.ExpiresAt, &row.CreatedAt,
		); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

// MarkSiesaPending deja la respuesta del paciente pendiente de SIESA (migración 047) en la última
// fila del historial de ese teléfono y cita, que Resolve acaba de escribir, con la conversación y la
// fecha y hora de la cita si llegan (la fila puede no tenerlas). Si no hay fila (Resolve falló)
// inserta una con status, para que la respuesta no se pierda. Volver a marcar una fila ya cerrada
// (p. ej. el agente reintenta y SIESA vuelve a fallar) reabre la espera y borra su desenlace.
// alreadyDeferred dice si esa fila ya había quedado pendiente antes (siesa_pending_since puesto): el
// paciente ya recibió el acuse.
func (r *NotificationRepo) MarkSiesaPending(ctx context.Context, row notifications.SiesaPendingRow, status string) (alreadyDeferred bool, err error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	var id int64
	err = tx.QueryRowContext(ctx,
		`SELECT id, siesa_pending_since IS NOT NULL FROM notification_history WHERE phone = ? AND appointment_id = ?
		 ORDER BY id DESC LIMIT 1 FOR UPDATE`, row.Phone, row.AppointmentID).Scan(&id, &alreadyDeferred)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		_, err = tx.ExecContext(ctx,
			`INSERT INTO notification_history
			   (phone, type, appointment_id, appointment_slot, bird_message_id, conversation_id, contact_phone, status, created_at,
			    siesa_pending, siesa_pending_since, siesa_last_error_at)
			 VALUES (?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), ?, NOW(), ?, NOW(), NOW())`,
			row.Phone, row.Type, row.AppointmentID, row.AppointmentSlot, row.BirdMessageID, row.ConversationID, row.ContactPhone,
			status, row.Action)
	case err == nil:
		_, err = tx.ExecContext(ctx,
			`UPDATE notification_history
			 SET siesa_pending = ?, siesa_pending_since = NOW(), siesa_attempts = 0,
			     siesa_last_error_at = NOW(), siesa_outcome = NULL,
			     conversation_id = COALESCE(NULLIF(?, ''), conversation_id),
			     appointment_slot = COALESCE(NULLIF(?, ''), appointment_slot)
			 WHERE id = ?`, row.Action, row.ConversationID, row.AppointmentSlot, id)
	}
	if err != nil {
		return false, err
	}
	return alreadyDeferred, tx.Commit()
}

// FindSiesaPending devuelve hasta limit respuestas pendientes de SIESA, la más vieja primero. El
// tiempo pendiente se mide en la BD (misma zona que siesa_pending_since).
func (r *NotificationRepo) FindSiesaPending(ctx context.Context, limit int) ([]notifications.SiesaPendingRow, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, phone, type, COALESCE(appointment_id, ''), COALESCE(appointment_slot, ''), COALESCE(bird_message_id, ''),
		        COALESCE(conversation_id, ''), COALESCE(contact_phone, ''), siesa_pending, siesa_attempts,
		        TIMESTAMPDIFF(SECOND, siesa_pending_since, NOW())
		 FROM notification_history
		 WHERE siesa_pending IS NOT NULL
		 ORDER BY id ASC
		 LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var result []notifications.SiesaPendingRow
	for rows.Next() {
		var row notifications.SiesaPendingRow
		var pendingSec int64
		if err := rows.Scan(
			&row.ID, &row.Phone, &row.Type, &row.AppointmentID, &row.AppointmentSlot, &row.BirdMessageID,
			&row.ConversationID, &row.ContactPhone, &row.Action, &row.Attempts, &pendingSec,
		); err != nil {
			return nil, err
		}
		row.PendingFor = time.Duration(pendingSec) * time.Second
		result = append(result, row)
	}
	return result, rows.Err()
}

// RecordSiesaAttempt suma un intento fallido a una respuesta que sigue pendiente de SIESA.
func (r *NotificationRepo) RecordSiesaAttempt(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE notification_history
		 SET siesa_attempts = siesa_attempts + 1, siesa_last_error_at = NOW()
		 WHERE id = ? AND siesa_pending IS NOT NULL`, id)
	return err
}

// FinishSiesaPending saca la respuesta de pendientes con su desenlace. Devuelve false si ya no
// estaba pendiente (nada que hacer).
func (r *NotificationRepo) FinishSiesaPending(ctx context.Context, id int64, outcome string) (bool, error) {
	res, err := r.db.ExecContext(ctx,
		`UPDATE notification_history SET siesa_pending = NULL, siesa_outcome = ?
		 WHERE id = ? AND siesa_pending IS NOT NULL`, outcome, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}
