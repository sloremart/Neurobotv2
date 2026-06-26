-- Revierte el seed de la migracion 024 (solo las dos sedes sembradas, no toca la tabla).
DELETE FROM center_locations WHERE name IN ('Sede Torre', 'Sede Imagenes');
