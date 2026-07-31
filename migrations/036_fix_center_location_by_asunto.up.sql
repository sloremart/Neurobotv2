-- Migration 036: corregir center_location_id usando asunto_id (fuente autoritativa).
--
-- El backfill de la migración 026 dependía de una coincidencia de texto (REGEXP_REPLACE
-- sobre la columna `direccion`). Si ese matching no fue 100% preciso en producción, algunos
-- CUPS de imagen pueden quedar apuntando a Sede Torre en lugar de Sede Imágenes.
--
-- Esta migración reescribe center_location_id basándose en el asunto_id:
--   asunto 2 (RX), 3 (TAC), 4 (Resonancia), 5 (Mamografía), 12 (PET/CT) → Sede Imágenes (id_sede=3)
--   todos los demás → Sede Torre (id_sede=2)
--
-- Fuente: CLAUDE.md § MAPA DE ASUNTOS. Idempotente: puede correr múltiples veces.

SET NAMES utf8mb4;

-- Procedimientos de imagen → Sede Imágenes
UPDATE cups_procedimientos
SET center_location_id = (SELECT id FROM center_locations WHERE name = 'Sede Imagenes' LIMIT 1),
    updated_at = updated_at
WHERE asunto_id IN (2, 3, 4, 5, 12);

-- Todo lo demás → Sede Torre
UPDATE cups_procedimientos
SET center_location_id = (SELECT id FROM center_locations WHERE name = 'Sede Torre' LIMIT 1),
    updated_at = updated_at
WHERE asunto_id NOT IN (2, 3, 4, 5, 12);
