-- Migration 032 DOWN
ALTER TABLE sessions
  DROP INDEX uq_active_phone,
  DROP COLUMN active_phone;
