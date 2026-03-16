-- Add column "agent_id" to table: "conversations"
ALTER TABLE `conversations` ADD COLUMN `agent_id` text NULL;
-- Add column "user_id" to table: "conversations"
ALTER TABLE `conversations` ADD COLUMN `user_id` integer NULL;
-- Add column "agent_id" to table: "scheduler_jobs"
ALTER TABLE `scheduler_jobs` ADD COLUMN `agent_id` text NULL;
-- Add column "user_id" to table: "scheduler_jobs"
ALTER TABLE `scheduler_jobs` ADD COLUMN `user_id` integer NULL;
-- Create "settings" table
CREATE TABLE `settings` (
  `key` text NULL,
  `value` text NOT NULL DEFAULT '{}',
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`key`)
);
-- Create "providers" table
CREATE TABLE `providers` (
  `id` text NULL,
  `name` text NOT NULL,
  `api_key` text NOT NULL DEFAULT '',
  `base_url` text NOT NULL DEFAULT '',
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`)
);
-- Create "agents" table
CREATE TABLE `agents` (
  `id` text NULL,
  `name` text NOT NULL,
  `provider_id` text NOT NULL,
  `model` text NOT NULL DEFAULT '',
  `model_strong` text NOT NULL DEFAULT '',
  `model_fast` text NOT NULL DEFAULT '',
  `system_prompt` text NOT NULL DEFAULT '',
  `workspace` text NOT NULL,
  `enabled` integer NOT NULL DEFAULT 1,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`provider_id`) REFERENCES `providers` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create "channels" table
CREATE TABLE `channels` (
  `id` text NULL,
  `enabled` integer NOT NULL DEFAULT 1,
  `config` text NOT NULL DEFAULT '{}',
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`)
);
-- Create "users" table
CREATE TABLE `users` (
  `id` integer NULL PRIMARY KEY AUTOINCREMENT,
  `external_id` text NOT NULL,
  `platform` text NOT NULL,
  `name` text NOT NULL DEFAULT '',
  `default_agent_id` text NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  CONSTRAINT `0` FOREIGN KEY (`default_agent_id`) REFERENCES `agents` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "users_external_id_platform" to table: "users"
CREATE UNIQUE INDEX `users_external_id_platform` ON `users` (`external_id`, `platform`);
-- Create "chat_agents" table
CREATE TABLE `chat_agents` (
  `platform` text NOT NULL,
  `chat_id` text NOT NULL,
  `agent_id` text NOT NULL,
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`platform`, `chat_id`),
  CONSTRAINT `0` FOREIGN KEY (`agent_id`) REFERENCES `agents` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create "user_agent_memory" table
CREATE TABLE `user_agent_memory` (
  `user_id` integer NOT NULL,
  `agent_id` text NOT NULL,
  `content` text NOT NULL DEFAULT '',
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`user_id`, `agent_id`),
  CONSTRAINT `0` FOREIGN KEY (`agent_id`) REFERENCES `agents` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION,
  CONSTRAINT `1` FOREIGN KEY (`user_id`) REFERENCES `users` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
