DROP INDEX idx_escalated_patient_activity ON sessions;

ALTER TABLE sessions
    DROP COLUMN agent_reminders_sent,
    DROP COLUMN last_agent_msg_at,
    DROP COLUMN last_patient_msg_at;
