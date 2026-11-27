ALTER TABLE nectar_waiting_cycles DROP COLUMN line;
ALTER TABLE escalations DROP COLUMN nectar_line;
ALTER TABLE chat_events DROP COLUMN nectar_line;
ALTER TABLE sessions DROP COLUMN nectar_line;
DROP TABLE nectar_conversation_line;
