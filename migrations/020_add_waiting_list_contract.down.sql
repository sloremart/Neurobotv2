-- Migration 020 DOWN: drop patient_contract from waiting_list
ALTER TABLE waiting_list DROP COLUMN patient_contract;
