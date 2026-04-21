-- Add column "enabled_builtin_skills" to table: "settings_agents"
ALTER TABLE `settings_agents` ADD COLUMN `enabled_builtin_skills` text NOT NULL DEFAULT '[]';
