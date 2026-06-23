-- Migration 020: add patient_contract to waiting_list
-- Stores sis_paci.contrato so the waiting-list MRC limit check can distinguish
-- MRC patients (contracts 5,6) from Evento, mirroring the main booking flow.
ALTER TABLE waiting_list
    ADD COLUMN patient_contract VARCHAR(20) NOT NULL DEFAULT '' AFTER patient_entity;
