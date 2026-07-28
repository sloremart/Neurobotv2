-- Protección contra "mensaje veneno" en el WAL de entrada.
--
-- Sin esto, un mensaje cuyo procesamiento MATA al proceso (panic, stack overflow, OOM) nunca se
-- marca 'done', así que el replay de arranque lo vuelve a encolar en el siguiente arranque, que
-- vuelve a morir: bucle de reinicio permanente sin intervención humana. Ocurrió el 28-jul-2026
-- (recursión de la capa de recuperación IA): ~2h de caída a partir de UN solo mensaje.
--
-- `attempts` cuenta los replays de arranque. Se incrementa ANTES de procesar (para que el intento
-- quede persistido aunque el proceso muera acto seguido); al superar el tope, la fila pasa a
-- 'poisoned' y deja de replayarse — el mensaje se pierde, pero el bot sobrevive y se emite alerta.
ALTER TABLE message_inbox
    ADD COLUMN attempts INT NOT NULL DEFAULT 0;
