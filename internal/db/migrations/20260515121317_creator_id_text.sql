-- Disable the enforcement of foreign-keys constraints
PRAGMA foreign_keys = off;
-- Create "new_settings_agents" table
CREATE TABLE `new_settings_agents` (
  `id` text NULL,
  `name` text NOT NULL,
  `model` text NOT NULL DEFAULT '',
  `model_strong` text NOT NULL DEFAULT '',
  `model_fast` text NOT NULL DEFAULT '',
  `system_prompt` text NOT NULL DEFAULT '',
  `soul` text NOT NULL DEFAULT '',
  `workspace` text NOT NULL,
  `sandbox` text NOT NULL DEFAULT '{}',
  `enabled_builtin_skills` text NOT NULL DEFAULT '[]',
  `scope` text NOT NULL DEFAULT 'system',
  `creator_id` text NOT NULL DEFAULT '',
  `enabled` integer NOT NULL DEFAULT 1,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`)
);
-- Copy rows from old table "settings_agents" to new temporary table "new_settings_agents"
INSERT INTO `new_settings_agents` (`id`, `name`, `model`, `model_strong`, `model_fast`, `system_prompt`, `soul`, `workspace`, `sandbox`, `enabled_builtin_skills`, `scope`, `creator_id`, `enabled`, `created_at`, `updated_at`) SELECT `id`, `name`, `model`, `model_strong`, `model_fast`, `system_prompt`, `soul`, `workspace`, `sandbox`, `enabled_builtin_skills`, `scope`, IFNULL(`creator_id`, '') AS `creator_id`, `enabled`, `created_at`, `updated_at` FROM `settings_agents`;
-- Drop "settings_agents" table after copying rows
DROP TABLE `settings_agents`;
-- Rename temporary table "new_settings_agents" to "settings_agents"
ALTER TABLE `new_settings_agents` RENAME TO `settings_agents`;
-- Enable back the enforcement of foreign-keys constraints
PRAGMA foreign_keys = on;
