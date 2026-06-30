-- Migration 032 (M3): garantizar UNA sola sesión ACTIVA/ESCALADA por teléfono a nivel BD.
-- Cierra el race: el worker y el NotificationManager usan candados por-teléfono DISTINTOS, así que dos
-- rutas concurrentes podían hacer FindActiveByPhone→(no hay)→Create para el mismo teléfono y dejar DOS
-- sesiones activas (contexto partido, lost-update, y un ticket de Bird que nunca se cierra).
--
-- MySQL no soporta índices parciales (CREATE UNIQUE INDEX ... WHERE), así que se usa una columna
-- generada que vale el teléfono SOLO cuando la sesión está activa/escalada y NULL en cualquier otro
-- estado. MySQL permite múltiples NULL en un índice UNIQUE, por lo que las sesiones completed/abandoned
-- no chocan; solo puede existir UNA fila activa/escalada por teléfono. El INSERT perdedor de la carrera
-- recibe el error 1062 y el caller re-lee la sesión existente.
SET NAMES utf8mb4;

-- PASO 1 — Dedup defensivo para PRODUCCIÓN: si ya existen teléfonos con >1 sesión activa/escalada
-- (resultado del race que esto corrige), el ADD UNIQUE KEY fallaría y dejaría la migración 'dirty'
-- (bot sin arrancar). Se conserva la más reciente (last_activity_at, desempate por id) y las demás se
-- marcan 'abandoned' — que es justo la que FindActiveByPhone ya devolvía, así que no cambia la UX.
-- Requiere MySQL 8.0+ (window functions); el stack del bot usa mysql:8.0.
UPDATE sessions s
JOIN (
    SELECT id FROM (
        SELECT id,
               ROW_NUMBER() OVER (PARTITION BY phone_number ORDER BY last_activity_at DESC, id DESC) AS rn
        FROM sessions
        WHERE status IN ('active', 'escalated')
    ) ranked
    WHERE ranked.rn > 1
) dups ON dups.id = s.id
SET s.status = 'abandoned', s.updated_at = NOW();

-- PASO 2 — Columna generada + índice único (ya sin duplicados que lo hagan fallar).
ALTER TABLE sessions
  ADD COLUMN active_phone VARCHAR(20)
    AS (CASE WHEN status IN ('active', 'escalated') THEN phone_number END) STORED,
  ADD UNIQUE KEY uq_active_phone (active_phone);
