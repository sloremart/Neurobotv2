ALTER TABLE waiting_list
    DROP INDEX idx_wl_appointment,
    DROP COLUMN appointment_id;
