ALTER TABLE escalations          DROP COLUMN contact_identifier;
ALTER TABLE chat_events          DROP COLUMN contact_identifier;
ALTER TABLE sessions             DROP INDEX idx_sessions_contact, DROP COLUMN contact_identifier;
ALTER TABLE notification_history DROP COLUMN contact_phone;
ALTER TABLE notification_pending DROP COLUMN contact_phone;
