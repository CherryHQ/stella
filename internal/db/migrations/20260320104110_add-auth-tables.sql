-- Add column "scope" to table: "settings_agents"
ALTER TABLE `settings_agents` ADD COLUMN `scope` text NOT NULL DEFAULT 'system';
-- Create "auth_users" table
CREATE TABLE `auth_users` (
  `id` integer NULL PRIMARY KEY AUTOINCREMENT,
  `username` text NOT NULL,
  `password_hash` text NOT NULL,
  `is_active` integer NOT NULL DEFAULT 1,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now'))
);
-- Create index "auth_users_username" to table: "auth_users"
CREATE UNIQUE INDEX `auth_users_username` ON `auth_users` (`username`);
-- Create "auth_roles" table
CREATE TABLE `auth_roles` (
  `id` text NULL,
  `name` text NOT NULL,
  `description` text NOT NULL DEFAULT '',
  `is_system` integer NOT NULL DEFAULT 0,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`)
);
-- Create "auth_user_roles" table
CREATE TABLE `auth_user_roles` (
  `user_id` integer NOT NULL,
  `role_id` text NOT NULL,
  PRIMARY KEY (`user_id`, `role_id`),
  CONSTRAINT `0` FOREIGN KEY (`role_id`) REFERENCES `auth_roles` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `1` FOREIGN KEY (`user_id`) REFERENCES `auth_users` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create "auth_identities" table
CREATE TABLE `auth_identities` (
  `id` integer NULL PRIMARY KEY AUTOINCREMENT,
  `user_id` integer NOT NULL,
  `platform` text NOT NULL,
  `external_id` text NOT NULL,
  `name` text NOT NULL DEFAULT '',
  `linked_at` text NOT NULL DEFAULT (datetime('now')),
  CONSTRAINT `0` FOREIGN KEY (`user_id`) REFERENCES `auth_users` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "auth_identities_platform_external_id" to table: "auth_identities"
CREATE UNIQUE INDEX `auth_identities_platform_external_id` ON `auth_identities` (`platform`, `external_id`);
-- Create "auth_policies" table
CREATE TABLE `auth_policies` (
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
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CHECK (effect IN ('allow', 'deny'))
);
-- Create "auth_user_agents" table
CREATE TABLE `auth_user_agents` (
  `user_id` integer NOT NULL,
  `agent_id` text NOT NULL,
  PRIMARY KEY (`user_id`, `agent_id`),
  CONSTRAINT `0` FOREIGN KEY (`agent_id`) REFERENCES `settings_agents` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `1` FOREIGN KEY (`user_id`) REFERENCES `auth_users` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create "auth_sessions" table
CREATE TABLE `auth_sessions` (
  `id` text NULL,
  `user_id` integer NOT NULL,
  `expires_at` text NOT NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`user_id`) REFERENCES `auth_users` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
