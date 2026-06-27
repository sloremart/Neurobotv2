-- Migration 028: retirar columnas legado de Antares de cups_procedimientos.
-- servicio_id, servicio y asunto_tipo NO las usa el bot: la clasificación del CUPS y el servicio
-- en SIESA se resuelven desde asunto_id (AsuntoPctos / tabla servicios), no desde el catálogo local.
-- El editor del dashboard ya dejó de gestionarlas. Se eliminan para no mantener datos muertos.
-- (idx_servicio_id se elimina automáticamente al dropear la columna.)

ALTER TABLE cups_procedimientos
  DROP COLUMN servicio_id,
  DROP COLUMN servicio,
  DROP COLUMN asunto_tipo;
