-- Migration 029: registrar el agente Bird asignado al escalar (para el SLA por agente).
-- El bot ya elige un agente concreto al escalar (pickLeastLoadedAgent); guardamos su id y nombre
-- en la sesión para poder medir, por agente: atención, devolución al bot y tiempos.

ALTER TABLE sessions
  ADD COLUMN agent_id   VARCHAR(100) NULL AFTER escalated_team,
  ADD COLUMN agent_name VARCHAR(150) NULL AFTER agent_id,
  ADD INDEX idx_agent (agent_id);
