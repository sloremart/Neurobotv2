-- Migration 047: tablas locales de los informes de asesores con Néctar (D5).
--
-- La sincronización copia aquí los tramos de espera (waiting-cycles) y el roster de agentes de
-- Néctar para que el dashboard los lea sin llamar a Néctar. Todo aditivo: con el puente apagado
-- las tablas quedan vacías y nada cambia. Las horas van en UTC (DATETIME, sin zona).
SET NAMES utf8mb4;

CREATE TABLE IF NOT EXISTS nectar_waiting_cycles (
  id                           BIGINT          NOT NULL,          -- id del tramo en Néctar
  conversation_id              BIGINT          NOT NULL,
  previous_cycle_id            BIGINT          NULL,
  team_id                      BIGINT          NULL,
  agent_id                     BIGINT          NULL,
  opened_at                    DATETIME(0)     NOT NULL,
  assigned_at                  DATETIME        NULL,
  first_response_at            DATETIME        NULL,
  closed_at                    DATETIME        NULL,
  close_reason                 VARCHAR(20)     NULL,
  backfilled                   TINYINT(1)      NOT NULL DEFAULT 0,
  contact_msgs_before_response INT             NOT NULL DEFAULT 0,
  escalation_id                BIGINT UNSIGNED NULL,              -- atención del bot a la que pertenece
  synced_at                    TIMESTAMP       NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  INDEX idx_nwc_conv_opened (conversation_id, opened_at),
  INDEX idx_nwc_opened (opened_at),
  INDEX idx_nwc_escalation (escalation_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS nectar_agents (
  id                 BIGINT       NOT NULL,
  name               VARCHAR(150) NOT NULL,
  active             TINYINT(1)   NOT NULL DEFAULT 1,
  presence           VARCHAR(20)  NOT NULL DEFAULT '',
  teams              JSON         NULL,
  open_conversations INT          NOT NULL DEFAULT 0,
  synced_at          TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- Estado de la sincronización: last_seen_opened_at, history_since, last_success_at.
CREATE TABLE IF NOT EXISTS nectar_sync_state (
  name       VARCHAR(40) NOT NULL,
  value      VARCHAR(64) NOT NULL,
  updated_at TIMESTAMP   NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- Lo que la sincronización aprende de Néctar sobre cada atención. ADD COLUMN NULL al final.
ALTER TABLE escalations ADD COLUMN contact_msgs_before_response INT NULL,
                        ADD COLUMN resolved_by VARCHAR(40) NULL,
                        ADD COLUMN escalated_by VARCHAR(40) NULL;
