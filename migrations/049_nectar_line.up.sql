-- Migration 049: la línea (canal de Néctar) por la que entra cada conversación.
--
-- El mismo bot atiende dos líneas en Néctar: "main" (la actual) y "legacy" (la línea vieja).
-- nectar_conversation_line guarda, por conversación, la línea con que se vio por primera vez
-- (una conversación nunca cambia de canal). Las demás columnas copian la línea para los informes.
-- Todo aditivo y anulable: NULL = "main", así que lo existente no cambia.
CREATE TABLE nectar_conversation_line (
  conversation_id BIGINT NOT NULL PRIMARY KEY,
  line VARCHAR(10) NOT NULL,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
ALTER TABLE sessions ADD COLUMN nectar_line VARCHAR(10) NULL;
ALTER TABLE chat_events ADD COLUMN nectar_line VARCHAR(10) NULL;
ALTER TABLE escalations ADD COLUMN nectar_line VARCHAR(10) NULL;
ALTER TABLE nectar_waiting_cycles ADD COLUMN line VARCHAR(10) NULL;
