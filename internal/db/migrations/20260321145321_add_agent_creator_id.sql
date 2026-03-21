-- Add column "creator_id" to table: "settings_agents"
ALTER TABLE `settings_agents` ADD COLUMN `creator_id` integer NOT NULL DEFAULT 0;
