-- Migration 024: Seed center_locations (Sede Torre / Sede Imagenes)
-- Reemplaza el seed que vivia en docker/mysql/init/02-seed-data.sql, que solo corria en el
-- primer arranque de un volumen MySQL vacio. Al moverlo a una migracion, el seed queda
-- versionado por golang-migrate y se aplica en cualquier despliegue.
-- Idempotente: guarda con WHERE NOT EXISTS por nombre de sede para no duplicar en una BD que
-- ya tenga las filas (p.ej. prod, sembrado por el init en su primer arranque). center_locations
-- no tiene indice unico sobre name, por eso el guard explicito en vez de ON DUPLICATE KEY.

SET NAMES utf8mb4;

INSERT INTO center_locations (name, address, phone, google_maps_url, is_active)
SELECT 'Sede Torre', 'Calle 35 No 36-26 - Barrio Barzal Alto', '', 'https://maps.app.goo.gl/eVNp9t7wY8DhgUhR6', TRUE
FROM DUAL
WHERE NOT EXISTS (SELECT 1 FROM center_locations WHERE name = 'Sede Torre');

INSERT INTO center_locations (name, address, phone, google_maps_url, is_active)
SELECT 'Sede Imagenes', 'Calle 34 No 38-47 - Barrio Barzal Alto', '', 'https://maps.app.goo.gl/MZqCxVoKAgwrnUVh7', TRUE
FROM DUAL
WHERE NOT EXISTS (SELECT 1 FROM center_locations WHERE name = 'Sede Imagenes');
