-- notification_history: traza de auditoría de cada notificación al resolverse (confirmada,
-- cancelada, reprogramada, expirada, escalada). notification_pending guarda solo lo PENDIENTE
-- activo y se vacía al resolver; al resolver, la fila se MUEVE aquí con su estado final y su
-- conversation_id, para tener evidencia (en qué chat, qué desenlace) sin que la tabla activa crezca.
-- Una tarea de limpieza borra los registros con más de 45 días.
CREATE TABLE IF NOT EXISTS notification_history (
    id              BIGINT AUTO_INCREMENT PRIMARY KEY,
    phone           VARCHAR(20) NOT NULL,
    type            VARCHAR(20) NOT NULL,
    appointment_id  VARCHAR(50) NULL,
    waiting_list_id VARCHAR(36) NULL,
    bird_message_id VARCHAR(100) NULL,
    conversation_id VARCHAR(100) NULL,
    status          VARCHAR(30) NOT NULL,
    created_at      TIMESTAMP NULL,
    resolved_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,

    INDEX idx_phone (phone),
    INDEX idx_status (status),
    INDEX idx_resolved (resolved_at),
    INDEX idx_appointment (appointment_id)
);
