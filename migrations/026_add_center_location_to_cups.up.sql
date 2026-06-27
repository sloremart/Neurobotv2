-- Migration 026: relacionar cups_procedimientos con center_locations por ID (no por texto).
-- La dirección de cada CUPS pasa a ser una FK a la sede (center_locations), que tiene la
-- dirección física + Google Maps. Así el bot resuelve la dirección del mensaje de WhatsApp por
-- id (exacto), sin matching difuso de texto. La columna `direccion` se conserva en esta migración
-- (necesaria para el backfill) y se retira en la 027, una vez poblado center_location_id.

SET NAMES utf8mb4;

ALTER TABLE cups_procedimientos
  ADD COLUMN center_location_id INT NULL AFTER direccion,
  ADD INDEX idx_center_location (center_location_id),
  ADD CONSTRAINT fk_cups_center_location FOREIGN KEY (center_location_id)
      REFERENCES center_locations(id) ON DELETE SET NULL;

-- Backfill: SOLO puebla la columna nueva (center_location_id). NO modifica datos existentes:
--  - `direccion` y demás columnas se conservan intactas.
--  - `updated_at = updated_at` preserva la fecha de actualización original (evita el ON UPDATE).
-- Emparejamiento por número de calle (solo dígitos); COLLATE concilia las collations de ambas tablas.
UPDATE cups_procedimientos c
JOIN center_locations cl
  ON REGEXP_REPLACE(c.direccion, '[^0-9]', '') = REGEXP_REPLACE(cl.address, '[^0-9]', '') COLLATE utf8mb4_unicode_ci
SET c.center_location_id = cl.id, c.updated_at = c.updated_at
WHERE c.direccion IS NOT NULL AND c.direccion <> '';
