-- Migration 047: respuesta al recordatorio pendiente de aplicar en SIESA.
--
-- Si el paciente pulsa «Confirmar» o «Cancelar» y SIESA no responde (lectura o escritura de la
-- cita), la respuesta queda en su fila de notification_history marcada con la acción por aplicar,
-- y un proceso de fondo la aplica cuando SIESA vuelve. Aditivo: las filas existentes quedan con
-- siesa_pending NULL (nada pendiente) y nada cambia.
--
--   siesa_pending        acción por aplicar: 'confirm' | 'cancel'; NULL = nada pendiente
--   siesa_pending_since  cuándo quedó pendiente (el tope de espera cuenta desde aquí)
--   siesa_attempts       intentos fallidos del proceso de fondo
--   siesa_last_error_at  último fallo de SIESA (el de la respuesta o el de un intento)
--   siesa_outcome        desenlace al dejar de estar pendiente: applied | already_applied |
--                        not_applicable | gave_up
ALTER TABLE notification_history
  ADD COLUMN siesa_pending       VARCHAR(10) NULL,
  ADD COLUMN siesa_pending_since TIMESTAMP   NULL,
  ADD COLUMN siesa_attempts      INT         NOT NULL DEFAULT 0,
  ADD COLUMN siesa_last_error_at TIMESTAMP   NULL,
  ADD COLUMN siesa_outcome       VARCHAR(20) NULL,
  ADD INDEX idx_siesa_pending (siesa_pending);
