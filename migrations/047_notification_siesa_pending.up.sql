-- Migration 047: respuesta al recordatorio pendiente de aplicar en SIESA.
--
-- Si el paciente pulsa «Confirmar» o «Cancelar» y SIESA no responde (lectura o escritura de la
-- cita), la respuesta queda en su fila de notification_history marcada con la acción por aplicar,
-- y un proceso de fondo la retoma cuando SIESA vuelve. Aditivo: las filas existentes quedan con
-- siesa_pending NULL (nada pendiente) y nada cambia.
--
--   appointment_slot     fecha y hora de la cita recordada (TimeSlot YYYYMMDDHHmm). Va en el pending
--                        desde el envío y Resolve la copia al historial: si al volver SIESA la cita
--                        tiene otra fecha u hora (la movieron con el mismo id), no se aplica nada.
--   siesa_pending        acción por aplicar: 'confirm' | 'cancel'; NULL = nada pendiente
--   siesa_pending_since  cuándo quedó pendiente (el tope de espera cuenta desde aquí)
--   siesa_attempts       intentos fallidos del proceso de fondo
--   siesa_last_error_at  último fallo de SIESA (el de la respuesta o el de un intento)
--   siesa_outcome        desenlace al dejar de estar pendiente: applied | cancel_prompted |
--                        already_applied | not_applicable | gave_up | unattended (la cita pasó
--                        con el paciente siempre conversando en Néctar: sin escalar)
ALTER TABLE notification_pending
  ADD COLUMN appointment_slot    VARCHAR(12) NULL;
ALTER TABLE notification_history
  ADD COLUMN appointment_slot    VARCHAR(12) NULL,
  ADD COLUMN siesa_pending       VARCHAR(10) NULL,
  ADD COLUMN siesa_pending_since TIMESTAMP   NULL,
  ADD COLUMN siesa_attempts      INT         NOT NULL DEFAULT 0,
  ADD COLUMN siesa_last_error_at TIMESTAMP   NULL,
  ADD COLUMN siesa_outcome       VARCHAR(20) NULL,
  ADD INDEX idx_siesa_pending (siesa_pending);
