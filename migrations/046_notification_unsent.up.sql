-- Migration 046: marca del pending de un recordatorio que no salió.
--
-- Cuando el envío del recordatorio del día antes falla, el bot deja igual un pending de
-- confirmación bajo el teléfono para que el IVR de las 13:00 llame al paciente. Con esa marca, el
-- mensaje que escriba el paciente sigue el flujo normal del bot (no se le reenvían los botones de
-- una plantilla que nunca recibió); solo el botón de la plantilla cuenta como respuesta. Se guarda
-- para que sobreviva a un reinicio entre el fallo y el IVR. Aditivo: 0 = todo como siempre.
ALTER TABLE notification_pending ADD COLUMN unsent TINYINT(1) NOT NULL DEFAULT 0;
ALTER TABLE notification_history ADD COLUMN unsent TINYINT(1) NOT NULL DEFAULT 0;
