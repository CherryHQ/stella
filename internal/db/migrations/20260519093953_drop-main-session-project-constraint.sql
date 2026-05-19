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
  PRIMARY KEY (`id`)
);
-- Copy rows from old table "ctx_conversations" to new temporary table "new_ctx_conversations"
INSERT INTO `new_ctx_conversations` (`id`, `session_id`, `title`, `channel`, `source`, `project_id`, `archived`, `last_active`, `bootstrapped_at`, `agent_id`, `user_id`, `created_at`, `updated_at`) SELECT `id`, `session_id`, `title`, `channel`, `source`, `project_id`, `archived`, `last_active`, `bootstrapped_at`, `agent_id`, `user_id`, `created_at`, `updated_at` FROM `ctx_conversations`;
-- Drop "ctx_conversations" table after copying rows
DROP TABLE `ctx_conversations`;
-- Rename temporary table "new_ctx_conversations" to "ctx_conversations"
ALTER TABLE `new_ctx_conversations` RENAME TO `ctx_conversations`;
-- Create index "ctx_conversations_session_id" to table: "ctx_conversations"
CREATE UNIQUE INDEX `ctx_conversations_session_id` ON `ctx_conversations` (`session_id`);
-- Create index "idx_one_main_per_agent_user" to table: "ctx_conversations"
CREATE UNIQUE INDEX `idx_one_main_per_agent_user` ON `ctx_conversations` (`agent_id`, `user_id`) WHERE source = 'main' AND archived = 0;
-- Enable back the enforcement of foreign-keys constraints
PRAGMA foreign_keys = on;
