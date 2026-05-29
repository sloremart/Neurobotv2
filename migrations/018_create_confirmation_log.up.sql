CREATE TABLE IF NOT EXISTS confirmation_log (
    id              BIGINT AUTO_INCREMENT PRIMARY KEY,
    appointment_id  VARCHAR(20) NOT NULL,
    patient_id      VARCHAR(20) NOT NULL,
    confirmed_at    DATETIME NOT NULL,
    channel         ENUM('whatsapp', 'ivr') NOT NULL,
    channel_id      VARCHAR(100) NULL,
    created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,

    INDEX idx_appointment (appointment_id),
    INDEX idx_patient (patient_id),
    INDEX idx_confirmed_at (confirmed_at),
    INDEX idx_channel (channel)
);
