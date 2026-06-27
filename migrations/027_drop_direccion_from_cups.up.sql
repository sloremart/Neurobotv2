-- Migration 027: retirar la columna `direccion` (texto) de cups_procedimientos.
-- Tras backfillear center_location_id (026), la dirección de texto es redundante: el bot resuelve
-- la dirección del mensaje de WhatsApp por el FK a center_locations. La dirección queda normalizada
-- (vive solo en center_locations), no duplicada como texto en cada CUPS.
--
-- PRECAUCIÓN (prod): antes de desplegar esta migración, verificar que NO queden CUPS activos sin
-- sede (perderían su dirección al dropear). Query de control:
--   SELECT COUNT(*) FROM cups_procedimientos WHERE activo=1 AND center_location_id IS NULL;
-- Si da > 0, asignarles la sede en el editor del dashboard antes de desplegar.

ALTER TABLE cups_procedimientos DROP COLUMN direccion;
