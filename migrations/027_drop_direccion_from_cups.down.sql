-- Restaura la columna direccion (vacía). El texto original NO se puede recuperar: la dirección
-- ahora se deriva de center_location_id (FK a center_locations). Solo recrea el esquema.
ALTER TABLE cups_procedimientos ADD COLUMN direccion VARCHAR(100) NULL AFTER preparacion;
