-- Disable the enforcement of foreign-keys constraints
PRAGMA foreign_keys = off;
-- Create "new_settings_channels" table
CREATE TABLE `new_settings_channels` (
  `id` text NULL,
  `type` text NOT NULL DEFAULT '',
  `agent_id` text NULL,
  `enabled` integer NOT NULL DEFAULT 1,
  `config` text NOT NULL DEFAULT '{}',
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`agent_id`) REFERENCES `settings_agents` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL
);
-- Copy rows from old table "settings_channels" to new temporary table "new_settings_channels"
INSERT INTO `new_settings_channels` (`id`, `enabled`, `config`, `created_at`, `updated_at`) SELECT `id`, `enabled`, `config`, `created_at`, `updated_at` FROM `settings_channels`;
-- Drop "settings_channels" table after copying rows
DROP TABLE `settings_channels`;
-- Rename temporary table "new_settings_channels" to "settings_channels"
ALTER TABLE `new_settings_channels` RENAME TO `settings_channels`;
-- Create "new_settings_channel_agents" table
CREATE TABLE `new_settings_channel_agents` (
  `channel_id` text NOT NULL DEFAULT '',
  `platform` text NOT NULL,
  `chat_id` text NOT NULL,
  `agent_id` text NOT NULL,
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`channel_id`, `platform`, `chat_id`),
  CONSTRAINT `0` FOREIGN KEY (`agent_id`) REFERENCES `settings_agents` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Copy rows from old table "settings_channel_agents" to new temporary table "new_settings_channel_agents"
INSERT INTO `new_settings_channel_agents` (`platform`, `chat_id`, `agent_id`, `updated_at`) SELECT `platform`, `chat_id`, `agent_id`, `updated_at` FROM `settings_channel_agents`;
-- Drop "settings_channel_agents" table after copying rows
DROP TABLE `settings_channel_agents`;
-- Rename temporary table "new_settings_channel_agents" to "settings_channel_agents"
ALTER TABLE `new_settings_channel_agents` RENAME TO `settings_channel_agents`;
-- Enable back the enforcement of foreign-keys constraints
PRAGMA foreign_keys = on;
