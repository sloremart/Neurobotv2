-- Revertir requiere que no queden filas 'unreachable' (se reclasifican a 'expired', el terminal más afín).
UPDATE waiting_list SET status = 'expired' WHERE status = 'unreachable';
ALTER TABLE waiting_list
    MODIFY COLUMN status ENUM('waiting','notified','scheduled','declined','expired','duplicate_found','pending_agent') DEFAULT 'waiting';
