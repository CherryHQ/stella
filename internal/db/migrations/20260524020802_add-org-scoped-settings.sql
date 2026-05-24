-- Disable the enforcement of foreign-keys constraints
PRAGMA foreign_keys = off;
-- Create "new_auth_policies" table
CREATE TABLE `new_auth_policies` (
  `id` text NULL,
  `name` text NOT NULL,
  `effect` text NOT NULL,
  `subjects` text NOT NULL DEFAULT '{}',
  `actions` text NOT NULL DEFAULT '[]',
  `resources` text NOT NULL DEFAULT '[]',
  `conditions` text NOT NULL DEFAULT '{}',
  `priority` integer NOT NULL DEFAULT 0,
  `is_system` integer NOT NULL DEFAULT 0,
  `enabled` integer NOT NULL DEFAULT 1,
  `org_id` text NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`org_id`) REFERENCES `auth_organization` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CHECK (effect IN ('allow', 'deny'))
);
-- Copy rows from old table "auth_policies" to new temporary table "new_auth_policies"
INSERT INTO `new_auth_policies` (`id`, `name`, `effect`, `subjects`, `actions`, `resources`, `conditions`, `priority`, `is_system`, `enabled`, `created_at`) SELECT `id`, `name`, `effect`, `subjects`, `actions`, `resources`, `conditions`, `priority`, `is_system`, `enabled`, `created_at` FROM `auth_policies`;
-- Drop "auth_policies" table after copying rows
DROP TABLE `auth_policies`;
-- Rename temporary table "new_auth_policies" to "auth_policies"
ALTER TABLE `new_auth_policies` RENAME TO `auth_policies`;
-- Create index "idx_auth_policies_org_id" to table: "auth_policies"
CREATE INDEX `idx_auth_policies_org_id` ON `auth_policies` (`org_id`);
-- Create "new_settings_providers" table
CREATE TABLE `new_settings_providers` (
  `id` text NULL,
  `type` text NOT NULL,
  `name` text NOT NULL,
  `enabled` integer NOT NULL DEFAULT 1,
  `config` text NOT NULL DEFAULT '{}',
  `org_id` text NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`org_id`) REFERENCES `auth_organization` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL
);
-- Copy rows from old table "settings_providers" to new temporary table "new_settings_providers"
INSERT INTO `new_settings_providers` (`id`, `type`, `name`, `enabled`, `config`, `created_at`, `updated_at`) SELECT `id`, `type`, `name`, `enabled`, `config`, `created_at`, `updated_at` FROM `settings_providers`;
-- Drop "settings_providers" table after copying rows
DROP TABLE `settings_providers`;
-- Rename temporary table "new_settings_providers" to "settings_providers"
ALTER TABLE `new_settings_providers` RENAME TO `settings_providers`;
-- Create index "idx_settings_providers_org_id" to table: "settings_providers"
CREATE INDEX `idx_settings_providers_org_id` ON `settings_providers` (`org_id`);
-- Create "new_settings_channels" table
CREATE TABLE `new_settings_channels` (
  `id` text NULL,
  `type` text NOT NULL DEFAULT '',
  `agent_id` text NULL,
  `enabled` integer NOT NULL DEFAULT 1,
  `config` text NOT NULL DEFAULT '{}',
  `org_id` text NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`org_id`) REFERENCES `auth_organization` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `1` FOREIGN KEY (`agent_id`) REFERENCES `settings_agents` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL
);
-- Copy rows from old table "settings_channels" to new temporary table "new_settings_channels"
INSERT INTO `new_settings_channels` (`id`, `type`, `agent_id`, `enabled`, `config`, `created_at`, `updated_at`) SELECT `id`, `type`, `agent_id`, `enabled`, `config`, `created_at`, `updated_at` FROM `settings_channels`;
-- Drop "settings_channels" table after copying rows
DROP TABLE `settings_channels`;
-- Rename temporary table "new_settings_channels" to "settings_channels"
ALTER TABLE `new_settings_channels` RENAME TO `settings_channels`;
-- Create index "idx_settings_channels_org_id" to table: "settings_channels"
CREATE INDEX `idx_settings_channels_org_id` ON `settings_channels` (`org_id`);
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
  `org_id` text NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`org_id`) REFERENCES `auth_organization` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL
);
-- Copy rows from old table "settings_agents" to new temporary table "new_settings_agents"
INSERT INTO `new_settings_agents` (`id`, `name`, `model`, `model_strong`, `model_fast`, `system_prompt`, `soul`, `workspace`, `sandbox`, `enabled_builtin_skills`, `scope`, `creator_id`, `enabled`, `created_at`, `updated_at`) SELECT `id`, `name`, `model`, `model_strong`, `model_fast`, `system_prompt`, `soul`, `workspace`, `sandbox`, `enabled_builtin_skills`, `scope`, `creator_id`, `enabled`, `created_at`, `updated_at` FROM `settings_agents`;
-- Drop "settings_agents" table after copying rows
DROP TABLE `settings_agents`;
-- Rename temporary table "new_settings_agents" to "settings_agents"
ALTER TABLE `new_settings_agents` RENAME TO `settings_agents`;
-- Create index "idx_settings_agents_org_id" to table: "settings_agents"
CREATE INDEX `idx_settings_agents_org_id` ON `settings_agents` (`org_id`);
-- Enable back the enforcement of foreign-keys constraints
PRAGMA foreign_keys = on;
