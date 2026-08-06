-- Estado 'unreachable': el envío de la notificación de lista de espera falló de forma PERMANENTE
-- (identificador no contactable o 4xx de Bird). Saca la entrada del pool de reintentos diarios:
-- cada reintento contra Bird se cobra aunque falle, y un fallo permanente nunca va a entregar.
ALTER TABLE waiting_list
    MODIFY COLUMN status ENUM('waiting','notified','scheduled','declined','expired','duplicate_found','pending_agent','unreachable') DEFAULT 'waiting';
