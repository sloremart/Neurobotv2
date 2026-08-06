-- Vínculo PERMANENTE lista de espera → cita creada. El KPI "recuperación de cupos cancelados"
-- necesita saber qué citas nacieron de la lista de espera; el vínculo existía solo en flow_events
-- (lista_espera/booked ref_id), que se purga a los 45 días.
ALTER TABLE waiting_list
    ADD COLUMN appointment_id VARCHAR(64) NULL AFTER status,
    ADD INDEX idx_wl_appointment (appointment_id);
