-- Migration 044: el teléfono real del paciente cuando la conversación va por Néctar.
--
-- Con Néctar la clave de la sesión y del pending es "nectar:<id>", no el teléfono. Estas columnas
-- guardan el teléfono E.164 del paciente (contact.identifier del estado de Néctar) para que la
-- idempotencia por teléfono y el IVR de las 13:00 sigan funcionando. Todo aditivo y anulable:
-- en Bird quedan en NULL y nada cambia.
--
-- VARCHAR(63) como los demás identificadores de contacto (migración 040). ADD COLUMN NULL al final
-- es instantáneo en MySQL 8, también en chat_events (tabla grande).
ALTER TABLE notification_pending ADD COLUMN contact_phone VARCHAR(63) NULL;
ALTER TABLE notification_history ADD COLUMN contact_phone VARCHAR(63) NULL;
ALTER TABLE sessions             ADD COLUMN contact_identifier VARCHAR(63) NULL,
                                 ADD INDEX idx_sessions_contact (contact_identifier);
ALTER TABLE chat_events          ADD COLUMN contact_identifier VARCHAR(63) NULL;
ALTER TABLE escalations          ADD COLUMN contact_identifier VARCHAR(63) NULL;
