-- Migration 026 DOWN: quitar la relación a center_locations.
ALTER TABLE cups_procedimientos
  DROP FOREIGN KEY fk_cups_center_location,
  DROP COLUMN center_location_id;
