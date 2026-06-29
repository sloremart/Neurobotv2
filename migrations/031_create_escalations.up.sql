-- Migration 031: registro POR ESCALACIÓN (no por sesión). Una conversación puede escalar varias
-- veces; cada escalación es una fila. Guarda el paso del chat donde se escaló (from_state) para
-- medir en qué punto se confunden más los pacientes, y las marcas de tiempo para el SLA real.
SET NAMES utf8mb4;

CREATE TABLE IF NOT EXISTS escalations (
  id                 BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  session_id         VARCHAR(64)  NOT NULL,
  phone_number       VARCHAR(20)  NOT NULL,
  from_state         VARCHAR(64)  NULL,          -- paso del chat donde se escaló
  team_id            VARCHAR(100) NULL,
  agent_id           VARCHAR(100) NULL,
  agent_name         VARCHAR(150) NULL,
  escalated_at       TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
  first_agent_msg_at TIMESTAMP    NULL,          -- primera respuesta del agente (TTFR)
  last_agent_msg_at  TIMESTAMP    NULL,
  resumed_at         TIMESTAMP    NULL,          -- cuándo se devolvió al bot (resume)
  outcome            VARCHAR(20)  NULL,          -- returned | completed | expired | NULL (abierta)
  created_at         TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  INDEX idx_esc_escalated_at (escalated_at),
  INDEX idx_esc_phone_open (phone_number, outcome),
  INDEX idx_esc_agent (agent_id),
  INDEX idx_esc_from_state (from_state),
  INDEX idx_esc_session (session_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- Backfill histórico: una fila por sesión YA escalada (la columna por-sesión solo guarda la última
-- escalación, así que el histórico queda con una fila por sesión; a partir de aquí el bot escribe una
-- fila por cada escalación). El from_state se toma del último evento escalated_to_agent de la sesión.
-- El outcome se deriva del estado final: devuelta al bot > cerrada por agente > expirada > abierta.
INSERT INTO escalations
  (session_id, phone_number, from_state, team_id, agent_id, agent_name,
   escalated_at, first_agent_msg_at, last_agent_msg_at, resumed_at, outcome, created_at)
SELECT
  s.id, s.phone_number,
  (SELECT JSON_UNQUOTE(JSON_EXTRACT(ce.event_data, '$.from_state'))
     FROM chat_events ce
     WHERE ce.session_id = s.id AND ce.event_type = 'escalated_to_agent'
     ORDER BY ce.created_at DESC LIMIT 1) AS from_state,
  s.escalated_team, s.agent_id, s.agent_name,
  s.escalated_at, s.first_agent_msg_at, s.last_agent_msg_at, s.resumed_at,
  CASE
    WHEN s.resumed_at IS NOT NULL THEN 'returned'
    WHEN s.status = 'completed'   THEN 'completed'
    WHEN s.status = 'abandoned'   THEN 'expired'
    ELSE NULL
  END AS outcome,
  s.escalated_at
FROM sessions s
WHERE s.escalated_at IS NOT NULL;
