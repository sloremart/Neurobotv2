-- Restaura las columnas legado (con defaults; los valores originales NO se recuperan).
ALTER TABLE cups_procedimientos
  ADD COLUMN servicio_id INT UNSIGNED NOT NULL DEFAULT 0 AFTER descripcion,
  ADD COLUMN servicio VARCHAR(50) NULL AFTER servicio_id,
  ADD COLUMN asunto_tipo ENUM('AC','AP') NOT NULL DEFAULT 'AP' AFTER asunto_id,
  ADD INDEX idx_servicio_id (servicio_id);
