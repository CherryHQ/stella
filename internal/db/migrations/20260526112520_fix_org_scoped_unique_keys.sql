-- Disable the enforcement of foreign-keys constraints
PRAGMA foreign_keys = off;
-- Create "new_settings_channel" table
CREATE TABLE `new_settings_channel` (
  `id` text NOT NULL,
  `type` text NOT NULL DEFAULT '',
  `agent_id` text NULL,
  `enabled` integer NOT NULL DEFAULT 1,
  `config` text NOT NULL DEFAULT '{}',
  `org_id` text NOT NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`, `org_id`),
  CONSTRAINT `0` FOREIGN KEY (`org_id`) REFERENCES `auth_organization` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `1` FOREIGN KEY (`agent_id`) REFERENCES `settings_agent` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL
);
-- Copy rows from old table "settings_channel" to new temporary table "new_settings_channel"
INSERT INTO `new_settings_channel` (`id`, `type`, `agent_id`, `enabled`, `config`, `org_id`, `created_at`, `updated_at`) SELECT `id`, `type`, `agent_id`, `enabled`, `config`, `org_id`, `created_at`, `updated_at` FROM `settings_channel`;
-- Drop "settings_channel" table after copying rows
DROP TABLE `settings_channel`;
-- Rename temporary table "new_settings_channel" to "settings_channel"
ALTER TABLE `new_settings_channel` RENAME TO `settings_channel`;
-- Create index "idx_settings_channels_org_id" to table: "settings_channel"
CREATE INDEX `idx_settings_channels_org_id` ON `settings_channel` (`org_id`);
-- Create "new_settings_channel_agent" table
CREATE TABLE `new_settings_channel_agent` (
  `channel_id` text NOT NULL DEFAULT '',
  `platform` text NOT NULL,
  `chat_id` text NOT NULL,
  `agent_id` text NOT NULL,
  `org_id` text NOT NULL,
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`channel_id`, `platform`, `chat_id`, `org_id`),
  CONSTRAINT `0` FOREIGN KEY (`org_id`) REFERENCES `auth_organization` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `1` FOREIGN KEY (`agent_id`) REFERENCES `settings_agent` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Copy rows from old table "settings_channel_agent" to new temporary table "new_settings_channel_agent"
INSERT INTO `new_settings_channel_agent` (`channel_id`, `platform`, `chat_id`, `agent_id`, `org_id`, `updated_at`) SELECT `channel_id`, `platform`, `chat_id`, `agent_id`, `org_id`, `updated_at` FROM `settings_channel_agent`;
-- Drop "settings_channel_agent" table after copying rows
DROP TABLE `settings_channel_agent`;
-- Rename temporary table "new_settings_channel_agent" to "settings_channel_agent"
ALTER TABLE `new_settings_channel_agent` RENAME TO `settings_channel_agent`;
-- Create index "idx_settings_channel_agents_org_id" to table: "settings_channel_agent"
CREATE INDEX `idx_settings_channel_agents_org_id` ON `settings_channel_agent` (`org_id`);
-- Create "new_skill" table
CREATE TABLE `new_skill` (
  `id` text NULL,
  `scope` text NOT NULL,
  `user_id` text NULL,
  `agent_id` text NULL,
  `name` text NOT NULL,
  `description` text NOT NULL,
  `status` text NOT NULL DEFAULT 'active',
  `disable_model_invocation` integer NOT NULL DEFAULT 0,
  `metadata` text NOT NULL DEFAULT '{}',
  `org_id` text NOT NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`org_id`) REFERENCES `auth_organization` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `1` FOREIGN KEY (`agent_id`) REFERENCES `settings_agent` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `2` FOREIGN KEY (`user_id`) REFERENCES `auth_user` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CHECK (scope IN ('system','agent','user')),
  CHECK (status IN ('draft','active','deprecated')),
  CHECK (
        (scope='system'  AND user_id IS NULL     AND agent_id IS NULL) OR
        (scope='agent'   AND user_id IS NULL     AND agent_id IS NOT NULL) OR
        (scope='user'    AND user_id IS NOT NULL)
    )
);
-- Copy rows from old table "skill" to new temporary table "new_skill"
INSERT INTO `new_skill` (`id`, `scope`, `user_id`, `agent_id`, `name`, `description`, `status`, `disable_model_invocation`, `metadata`, `org_id`, `created_at`, `updated_at`) SELECT `id`, `scope`, `user_id`, `agent_id`, `name`, `description`, `status`, `disable_model_invocation`, `metadata`, `org_id`, `created_at`, `updated_at` FROM `skill`;
-- Drop "skill" table after copying rows
DROP TABLE `skill`;
-- Rename temporary table "new_skill" to "skill"
ALTER TABLE `new_skill` RENAME TO `skill`;
-- Create index "idx_skills_owner_name" to table: "skill"
CREATE UNIQUE INDEX `idx_skills_owner_name` ON `skill` (`org_id`, `name`, `scope`, (ifnull(user_id, 0)), (ifnull(agent_id, '')));
-- Create index "idx_skills_visibility" to table: "skill"
CREATE INDEX `idx_skills_visibility` ON `skill` (`scope`, `user_id`, `agent_id`);
-- Create index "idx_skills_org_id" to table: "skill"
CREATE INDEX `idx_skills_org_id` ON `skill` (`org_id`);
-- Enable back the enforcement of foreign-keys constraints
PRAGMA foreign_keys = on;
