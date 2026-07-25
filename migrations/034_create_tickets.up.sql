-- Migration 034: tickets/incidencias que los admins reportan desde el DASHBOARD (errores del bot,
-- de la plataforma Bird en canales de WhatsApp/voz, o del propio dashboard). Cada ticket NUEVO dispara
-- una notificación a Telegram (la envía el dashboard). La tabla la administra el dashboard vía CRUD;
-- el bot solo la crea con esta migración (es el dueño del schema de neuro_bot).
SET NAMES utf8mb4;

CREATE TABLE IF NOT EXISTS tickets (
  id           BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  title        VARCHAR(200)  NOT NULL,                     -- resumen corto
  description  TEXT          NOT NULL,                     -- detalle del problema
  area         VARCHAR(20)   NOT NULL,                     -- bot | bird_wa | bird_voz | dashboard | otro
  severity     VARCHAR(10)   NOT NULL,                     -- baja | media | alta | critica
  status       VARCHAR(15)   NOT NULL DEFAULT 'abierto',   -- abierto | en_progreso | resuelto | cerrado
  reporter     VARCHAR(100)  NOT NULL DEFAULT '',          -- "reportado por" (texto libre)
  affected_ref VARCHAR(200)  NOT NULL DEFAULT '',          -- telefono/conversacion/agenda afectada (opcional)
  resolution   TEXT          NULL,                         -- nota de cierre
  created_at   TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at   TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  resolved_at  TIMESTAMP     NULL,
  PRIMARY KEY (id),
  INDEX idx_tickets_status (status),
  INDEX idx_tickets_area (area),
  INDEX idx_tickets_severity (severity),
  INDEX idx_tickets_created (created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
