ALTER TABLE medication_pending
    ADD COLUMN via_administracion VARCHAR(100) NOT NULL DEFAULT '' AFTER numero_autorizacion;
