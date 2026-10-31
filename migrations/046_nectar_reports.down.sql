ALTER TABLE escalations DROP COLUMN escalated_by, DROP COLUMN resolved_by, DROP COLUMN contact_msgs_before_response;
DROP TABLE IF EXISTS nectar_sync_state;
DROP TABLE IF EXISTS nectar_agents;
DROP TABLE IF EXISTS nectar_waiting_cycles;
