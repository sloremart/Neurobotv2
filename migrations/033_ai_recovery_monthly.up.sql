-- Contador de recuperaciones asistidas por IA por mes calendario (AI_RECOVERY_MONTHLY_LIMIT).
-- Fuente de verdad del tope mensual (control de gasto). El KPI se calcula de flow_events; esta
-- tabla es solo para el enforcement rápido en el hot-path.
CREATE TABLE IF NOT EXISTS ai_recovery_monthly (
    period     CHAR(7)   NOT NULL,           -- 'YYYY-MM'
    count      INT       NOT NULL DEFAULT 0, -- recuperaciones tomadas por la IA ese mes
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    PRIMARY KEY (period)
);
