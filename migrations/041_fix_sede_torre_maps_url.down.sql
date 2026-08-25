-- Revierte 041: restaura el URL incorrecto de Sede Torre (para rollback).
UPDATE center_locations
SET google_maps_url = 'https://maps.app.goo.gl/eVNp9t7wY8DhgUhR6'
WHERE name = 'Sede Torre';
