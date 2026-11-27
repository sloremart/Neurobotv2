-- Corrige chat_events.nectar_line de los eventos proactivos (recordatorios, avisos, IVR) registrados
-- con el teléfono de SIESA y sin sesión: salen por la línea principal, así que llevan 'main'.
-- Los que se registraron antes de encender el puente quedaron NULL; NULL = main para los KPI por
-- línea, pero con la marca explícita el filtro por línea no depende de esa convención.
--
-- Quién la pone: la persona que despliega, a mano, UNA vez, con la fecha-hora en que se encendió el
-- puente de Néctar (primer arranque con NECTAR_BASE_URL); antes de esa hora esos eventos eran de Bird.
-- Idempotente: solo toca filas con nectar_line NULL; correrla dos veces no cambia nada.
-- La lista es la misma de internal/repository/local/proactive_event_types.go (hay una prueba de paridad).

SET @desde = '2026-01-01 00:00:00';  -- CAMBIAR: fecha-hora de encendido del puente

UPDATE chat_events
SET nectar_line = 'main'
WHERE nectar_line IS NULL
  AND event_type IN (
    'reminder_prep_sent',
    'reminder_suppressed_no_whatsapp',
    'notification_send_failed',
    'notification_in_doubt',
    'notification_skipped_agent_owned',
    'notification_confirmed_ivr',
    'notification_cancelled_ivr',
    'notification_timeout'
  )
  AND created_at >= @desde;

SELECT ROW_COUNT() AS filas_corregidas;
