-- Migration 030 DOWN
ALTER TABLE sessions DROP COLUMN first_agent_msg_at;
