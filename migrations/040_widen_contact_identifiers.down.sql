-- Revertir a VARCHAR(20). OJO: falla si ya existen identificadores whatsappusername (>20 chars)
-- persistidos — en ese caso habría que depurarlos antes (es intencional: revertir truncando
-- silenciosamente corrompería los datos).
SET NAMES utf8mb4;

ALTER TABLE message_inbox             MODIFY phone        VARCHAR(20) NOT NULL;
ALTER TABLE sessions                  MODIFY phone_number VARCHAR(20) NOT NULL;
ALTER TABLE sessions                  MODIFY active_phone VARCHAR(20)
  AS (CASE WHEN status IN ('active', 'escalated') THEN phone_number END) STORED;
ALTER TABLE chat_events               MODIFY phone_number VARCHAR(20) NOT NULL;
ALTER TABLE communication_messages    MODIFY phone_number VARCHAR(20) NOT NULL;
ALTER TABLE communication_calls      MODIFY phone_number VARCHAR(20) NOT NULL;
ALTER TABLE waiting_list              MODIFY phone_number VARCHAR(20) NOT NULL;
ALTER TABLE notification_pending      MODIFY phone        VARCHAR(20) NOT NULL;
ALTER TABLE notification_history      MODIFY phone        VARCHAR(20) NOT NULL;
ALTER TABLE escalations               MODIFY phone_number VARCHAR(20) NOT NULL;
ALTER TABLE message_delivery_failures MODIFY phone_number VARCHAR(20) NOT NULL;
