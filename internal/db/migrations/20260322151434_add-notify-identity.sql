-- Disable the enforcement of foreign-keys constraints
PRAGMA foreign_keys = off;
-- Create "new_auth_users" table
CREATE TABLE `new_auth_users` (
  `id` integer NULL PRIMARY KEY AUTOINCREMENT,
  `username` text NOT NULL,
  `password_hash` text NOT NULL DEFAULT '',
  `role` text NOT NULL DEFAULT 'user',
  `is_active` integer NOT NULL DEFAULT 1,
  `default_agent_id` text NULL,
  `notify_identity_id` integer NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  CONSTRAINT `0` FOREIGN KEY (`notify_identity_id`) REFERENCES `auth_identities` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `1` FOREIGN KEY (`default_agent_id`) REFERENCES `settings_agents` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Copy rows from old table "auth_users" to new temporary table "new_auth_users"
INSERT INTO `new_auth_users` (`id`, `username`, `password_hash`, `role`, `is_active`, `default_agent_id`, `created_at`, `updated_at`) SELECT `id`, `username`, `password_hash`, `role`, `is_active`, `default_agent_id`, `created_at`, `updated_at` FROM `auth_users`;
-- Drop "auth_users" table after copying rows
DROP TABLE `auth_users`;
-- Rename temporary table "new_auth_users" to "auth_users"
ALTER TABLE `new_auth_users` RENAME TO `auth_users`;
-- Create index "auth_users_username" to table: "auth_users"
CREATE UNIQUE INDEX `auth_users_username` ON `auth_users` (`username`);
-- Enable back the enforcement of foreign-keys constraints
PRAGMA foreign_keys = on;
