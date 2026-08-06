-- Fallos de ENTREGA de WhatsApp por teléfono (webhook outbound de Bird, status delivery_failed).
-- Con >=2 fallos consecutivos los templates programados (recordatorios, lista de espera) se
-- suprimen: cada envío a un número sin WhatsApp se cobra y jamás llega. Un delivered/read (o
-- cualquier mensaje entrante del número) resetea el contador.
CREATE TABLE IF NOT EXISTS message_delivery_failures (
    phone_number         VARCHAR(20) NOT NULL PRIMARY KEY,
    consecutive_failures INT         NOT NULL DEFAULT 0,
    last_status          VARCHAR(50) NULL,
    last_failure_at      TIMESTAMP   NULL,
    updated_at           TIMESTAMP   NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
);
