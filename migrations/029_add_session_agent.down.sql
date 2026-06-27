-- Migration 029 DOWN: quitar el agente asignado de la sesión.
ALTER TABLE sessions
  DROP INDEX idx_agent,
  DROP COLUMN agent_name,
  DROP COLUMN agent_id;
