-- Migration 030: tiempo a la PRIMERA respuesta del agente (TTFR) en una escalación.
-- last_agent_msg_at se sobrescribe en cada mensaje del agente; first_agent_msg_at se sella una sola
-- vez (la primera respuesta) para medir el TTFR real. Aditiva.
ALTER TABLE sessions
  ADD COLUMN first_agent_msg_at TIMESTAMP NULL AFTER last_patient_msg_at;
