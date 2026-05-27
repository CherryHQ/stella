-- Disable the enforcement of foreign-keys constraints
PRAGMA foreign_keys = off;
-- Create "new_settings_plugin" table
CREATE TABLE `new_settings_plugin` (
  `id` text NOT NULL,
  `kind` text NOT NULL,
  `name` text NOT NULL,
  `enabled` integer NOT NULL DEFAULT 1,
  `config` text NOT NULL DEFAULT '{}',
  `org_id` text NOT NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`, `org_id`),
  CONSTRAINT `0` FOREIGN KEY (`org_id`) REFERENCES `auth_organization` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Copy rows from old table "settings_plugin" to new temporary table "new_settings_plugin"
INSERT INTO `new_settings_plugin` (`id`, `kind`, `name`, `enabled`, `config`, `org_id`, `created_at`, `updated_at`) SELECT `id`, `kind`, `name`, `enabled`, `config`, `org_id`, `created_at`, `updated_at` FROM `settings_plugin`;
-- Drop "settings_plugin" table after copying rows
DROP TABLE `settings_plugin`;
-- Rename temporary table "new_settings_plugin" to "settings_plugin"
ALTER TABLE `new_settings_plugin` RENAME TO `settings_plugin`;
-- Create index "idx_settings_plugins_org_id" to table: "settings_plugin"
CREATE INDEX `idx_settings_plugins_org_id` ON `settings_plugin` (`org_id`);
-- Enable back the enforcement of foreign-keys constraints
PRAGMA foreign_keys = on;
