-- flow_events: traza estructurada del recorrido de NEGOCIO de cada flujo, correlacionada por
-- trace_id (ver docs/OBSERVABILIDAD.md). Complementa chat_events (conversación cruda) y slog
-- (técnico); NO los reemplaza. Retención 45 días de crudos + rollup diario en flow_daily_stats.
--
-- `level` = tier de registro/filtro (1=error 2=outcome 3=milestone 4=full), distinto de
-- `outcome` (resultado: ok|blocked|error|escalated|retry|info). Índices deliberadamente pocos
-- (tabla de alta escritura); las consultas por outcome/reason filtran sobre el rango temporal.
CREATE TABLE IF NOT EXISTS flow_events (
    id          BIGINT AUTO_INCREMENT PRIMARY KEY,
    trace_id    VARCHAR(64)  NOT NULL,
    flow        VARCHAR(40)  NOT NULL,
    step        VARCHAR(60)  NOT NULL,
    level       TINYINT      NOT NULL,
    outcome     VARCHAR(20)  NOT NULL,
    reason      VARCHAR(60)  NULL,
    phone       VARCHAR(20)  NULL,
    ref_type    VARCHAR(30)  NULL,
    ref_id      VARCHAR(64)  NULL,
    attrs       JSON         NULL,
    created_at  TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),

    INDEX idx_trace   (trace_id, created_at),
    INDEX idx_flow    (flow, created_at),
    INDEX idx_phone   (phone, created_at),
    INDEX idx_created (created_at)
);

-- Agregados diarios: sobreviven a la purga de los crudos → tendencias de largo plazo.
CREATE TABLE IF NOT EXISTS flow_daily_stats (
    day      DATE        NOT NULL,
    flow     VARCHAR(40) NOT NULL,
    step     VARCHAR(60) NOT NULL,
    outcome  VARCHAR(20) NOT NULL,
    reason   VARCHAR(60) NOT NULL DEFAULT '',
    cnt      INT         NOT NULL,
    PRIMARY KEY (day, flow, step, outcome, reason)
);
