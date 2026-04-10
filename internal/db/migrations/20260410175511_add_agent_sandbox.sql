-- Add column "sandbox" to table: "settings_agents"
ALTER TABLE `settings_agents` ADD COLUMN `sandbox` text NOT NULL DEFAULT '{}';
