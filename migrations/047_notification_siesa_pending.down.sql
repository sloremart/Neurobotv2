ALTER TABLE notification_history
  DROP INDEX idx_siesa_pending,
  DROP COLUMN siesa_outcome,
  DROP COLUMN siesa_last_error_at,
  DROP COLUMN siesa_attempts,
  DROP COLUMN siesa_pending_since,
  DROP COLUMN siesa_pending;
