-- Add column "constraints" to table: "ctx_agent_memory"
ALTER TABLE `ctx_agent_memory` ADD COLUMN `constraints` text NOT NULL DEFAULT '[]';
