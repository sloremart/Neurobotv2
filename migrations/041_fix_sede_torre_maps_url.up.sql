-- Migration 041: corregir Google Maps URL de Sede Torre.
-- El URL almacenado apuntaba a Sede Imágenes en vez de Sede Torre.
-- Correcto: https://maps.app.goo.gl/YnHF8mneTySJqLsb9

UPDATE center_locations
SET google_maps_url = 'https://maps.app.goo.gl/YnHF8mneTySJqLsb9'
WHERE name = 'Sede Torre';
