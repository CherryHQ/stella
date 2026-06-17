-- Add column "agent_session_id" to table: "ctx_group_message"
ALTER TABLE `ctx_group_message` ADD COLUMN `agent_session_id` text NOT NULL DEFAULT '';
