-- Disable the enforcement of foreign-keys constraints
PRAGMA foreign_keys = off;
-- Create "new_ctx_conversations" table
CREATE TABLE `new_ctx_conversations` (
  `id` text NULL,
  `session_id` text NOT NULL,
  `title` text NULL,
  `channel` text NOT NULL DEFAULT '',
  `source` text NOT NULL DEFAULT 'chat',
  `project_id` text NULL,
  `archived` integer NOT NULL DEFAULT 0,
  `last_active` text NOT NULL DEFAULT (datetime('now')),
  `bootstrapped_at` text NULL,
  `agent_id` text NULL,
  `user_id` text NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CHECK (source != 'main' OR project_id IS NOT NULL)
);
-- Copy rows from old table "ctx_conversations" to new temporary table "new_ctx_conversations"
INSERT INTO `new_ctx_conversations` (`id`, `session_id`, `title`, `channel`, `archived`, `last_active`, `bootstrapped_at`, `agent_id`, `user_id`, `created_at`, `updated_at`) SELECT `id`, `session_id`, `title`, `channel`, `archived`, `last_active`, `bootstrapped_at`, `agent_id`, `user_id`, `created_at`, `updated_at` FROM `ctx_conversations`;
-- Backfill source for task sessions
UPDATE `new_ctx_conversations` SET source = 'task', channel = 'system'
  WHERE channel = 'task' OR session_id LIKE 'task:%';
-- Backfill source for scheduler sessions
UPDATE `new_ctx_conversations` SET source = 'scheduler', channel = 'system'
  WHERE session_id LIKE '%:scheduler:%' OR session_id LIKE 'scheduler:%';
-- Drop "ctx_conversations" table after copying rows
DROP TABLE `ctx_conversations`;
-- Rename temporary table "new_ctx_conversations" to "ctx_conversations"
ALTER TABLE `new_ctx_conversations` RENAME TO `ctx_conversations`;
-- Create index "ctx_conversations_session_id" to table: "ctx_conversations"
CREATE UNIQUE INDEX `ctx_conversations_session_id` ON `ctx_conversations` (`session_id`);
-- Create index "idx_one_main_per_project" to table: "ctx_conversations"
CREATE UNIQUE INDEX `idx_one_main_per_project` ON `ctx_conversations` (`project_id`) WHERE source = 'main' AND archived = 0 AND project_id IS NOT NULL;
-- Create "projects" table
CREATE TABLE `projects` (
  `id` text NULL,
  `agent_id` text NOT NULL,
  `user_id` text NOT NULL,
  `name` text NOT NULL,
  `base_dir` text NOT NULL,
  `description` text NULL,
  `archived` integer NOT NULL DEFAULT 0,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`)
);
-- Create index "projects_agent_id_user_id_name" to table: "projects"
CREATE UNIQUE INDEX `projects_agent_id_user_id_name` ON `projects` (`agent_id`, `user_id`, `name`);
-- Enable back the enforcement of foreign-keys constraints
PRAGMA foreign_keys = on;
