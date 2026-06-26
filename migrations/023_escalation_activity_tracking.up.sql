-- Escalation activity tracking: separar el silencio del paciente del silencio del agente.
-- last_patient_msg_at  → solo inbound del paciente; gobierna el cierre por inactividad del paciente.
-- last_agent_msg_at     → respuesta del agente (saliente humano); frena el recordatorio al agente.
-- agent_reminders_sent  → recordatorios enviados en la ventana de espera actual (reset con cada msg del paciente).
ALTER TABLE sessions
    ADD COLUMN last_patient_msg_at TIMESTAMP NULL AFTER last_activity_at,
    ADD COLUMN last_agent_msg_at   TIMESTAMP NULL AFTER last_patient_msg_at,
    ADD COLUMN agent_reminders_sent TINYINT NOT NULL DEFAULT 0 AFTER last_agent_msg_at;

-- Índice para el checker de sesiones escaladas (filtra por status y ordena por actividad del paciente).
CREATE INDEX idx_escalated_patient_activity ON sessions (status, last_patient_msg_at);
