-- Disable the enforcement of foreign-keys constraints
PRAGMA foreign_keys = off;
-- Create "new_settings_plugin_state" table
CREATE TABLE `new_settings_plugin_state` (
  `plugin_id` text NOT NULL,
  `scope_kind` text NOT NULL,
  `scope_id` text NOT NULL DEFAULT '',
  `state_key` text NOT NULL,
  `value` text NOT NULL DEFAULT '{}',
  `org_id` text NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`plugin_id`, `scope_kind`, `scope_id`, `state_key`, `org_id`),
  CONSTRAINT `0` FOREIGN KEY (`org_id`) REFERENCES `auth_organization` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Copy rows from old table "settings_plugin_state" to new temporary table "new_settings_plugin_state"
INSERT INTO `new_settings_plugin_state` (`plugin_id`, `scope_kind`, `scope_id`, `state_key`, `value`, `created_at`, `updated_at`) SELECT `plugin_id`, `scope_kind`, `scope_id`, `state_key`, `value`, `created_at`, `updated_at` FROM `settings_plugin_state`;
-- Drop "settings_plugin_state" table after copying rows
DROP TABLE `settings_plugin_state`;
-- Rename temporary table "new_settings_plugin_state" to "settings_plugin_state"
ALTER TABLE `new_settings_plugin_state` RENAME TO `settings_plugin_state`;
-- Create index "idx_settings_plugin_state_org_id" to table: "settings_plugin_state"
CREATE INDEX `idx_settings_plugin_state_org_id` ON `settings_plugin_state` (`org_id`);
-- Create "new_plugin_oauth_provider" table
CREATE TABLE `new_plugin_oauth_provider` (
  `id` text NULL,
  `provider_id` text NOT NULL,
  `client_id` text NOT NULL DEFAULT '',
  `client_secret_enc` text NOT NULL DEFAULT '',
  `redirect_url` text NOT NULL DEFAULT '',
  `org_id` text NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`org_id`) REFERENCES `auth_organization` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Copy rows from old table "plugin_oauth_provider" to new temporary table "new_plugin_oauth_provider"
INSERT INTO `new_plugin_oauth_provider` (`id`, `provider_id`, `client_id`, `client_secret_enc`, `redirect_url`, `created_at`, `updated_at`) SELECT `id`, `provider_id`, `client_id`, `client_secret_enc`, `redirect_url`, `created_at`, `updated_at` FROM `plugin_oauth_provider`;
-- Drop "plugin_oauth_provider" table after copying rows
DROP TABLE `plugin_oauth_provider`;
-- Rename temporary table "new_plugin_oauth_provider" to "plugin_oauth_provider"
ALTER TABLE `new_plugin_oauth_provider` RENAME TO `plugin_oauth_provider`;
-- Create index "plugin_oauth_provider_provider_id_org_id" to table: "plugin_oauth_provider"
CREATE UNIQUE INDEX `plugin_oauth_provider_provider_id_org_id` ON `plugin_oauth_provider` (`provider_id`, `org_id`);
-- Create index "idx_plugin_oauth_provider_org_id" to table: "plugin_oauth_provider"
CREATE INDEX `idx_plugin_oauth_provider_org_id` ON `plugin_oauth_provider` (`org_id`);
-- Enable back the enforcement of foreign-keys constraints
PRAGMA foreign_keys = on;
