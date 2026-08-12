-- Migration 040: ampliar los identificadores de contacto en TODA la tubería local:
-- VARCHAR(20) → VARCHAR(63).
--
-- POR QUÉ: WhatsApp tiene una función de privacidad ("proteger mi número") con la que el
-- paciente escribe SIN revelar su teléfono; Bird entrega identifierKey=whatsappusername con un
-- ID seudónimo (CO.xxx) de más de 20 caracteres. Con VARCHAR(20) ese identificador no cabía en
-- message_inbox y el mensaje se perdía (incidente H138-2/H139-1: 1406 → 500 → reintento eterno
-- de Bird → alertas Telegram sin fin). Los de ID corto (≤20) ya se atienden hoy vía
-- Conversations API (PR#35, validado en vivo): al ampliar, los de ID largo fluyen IGUAL.
--
-- POR QUÉ 63 Y NO 64: con utf8mb4 (4 bytes/char), 63 chars = 252 bytes ≤ 255 → el prefijo de
-- longitud sigue siendo de 1 byte y el ALTER es INPLACE/instantáneo en las tablas grandes
-- (chat_events, communication_messages). Con 64 (256 bytes) el prefijo pasa a 2 bytes y MySQL
-- reconstruye la tabla entera. flow_events NO se toca: guarda el teléfono ENMASCARADO
-- (tracer.go, ≤11 chars) y además carga 10M+ filas del incidente 28-jul.
--
-- El gate del webhook (pipelinePhoneCapacity, webhook_handler.go) queda sincronizado en 63:
-- cualquier identificador aún más largo se sigue descartando en el borde, medible y sin bucle.
SET NAMES utf8mb4;

ALTER TABLE message_inbox             MODIFY phone        VARCHAR(63) NOT NULL;
ALTER TABLE sessions                  MODIFY phone_number VARCHAR(63) NOT NULL;
-- active_phone es columna GENERADA (migración 032, índice único de sesión-activa-por-teléfono):
-- se re-declara con su expresión original. Este ALTER usa COPY (los generados STORED no admiten
-- INPLACE), pero sessions es una tabla chica — coste despreciable.
ALTER TABLE sessions                  MODIFY active_phone VARCHAR(63)
  AS (CASE WHEN status IN ('active', 'escalated') THEN phone_number END) STORED;
ALTER TABLE chat_events               MODIFY phone_number VARCHAR(63) NOT NULL;
ALTER TABLE communication_messages    MODIFY phone_number VARCHAR(63) NOT NULL;
ALTER TABLE communication_calls       MODIFY phone_number VARCHAR(63) NOT NULL;
ALTER TABLE waiting_list              MODIFY phone_number VARCHAR(63) NOT NULL;
ALTER TABLE notification_pending      MODIFY phone        VARCHAR(63) NOT NULL;
ALTER TABLE notification_history      MODIFY phone        VARCHAR(63) NOT NULL;
ALTER TABLE escalations               MODIFY phone_number VARCHAR(63) NOT NULL;
ALTER TABLE message_delivery_failures MODIFY phone_number VARCHAR(63) NOT NULL;
