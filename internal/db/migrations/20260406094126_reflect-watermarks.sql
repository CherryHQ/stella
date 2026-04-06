-- Disable the enforcement of foreign-keys constraints
PRAGMA foreign_keys = off;
-- Create "reflect_watermarks" table
CREATE TABLE `reflect_watermarks` (
  `session_id` text NOT NULL,
  `reviewed_at` text NOT NULL,
  PRIMARY KEY (`session_id`)
);
-- Migrate existing review watermarks before dropping the old column
INSERT INTO `reflect_watermarks` (`session_id`, `reviewed_at`)
SELECT `session_id`, `self_improve_reviewed_at`
FROM `ctx_conversations`
WHERE `self_improve_reviewed_at` IS NOT NULL AND `self_improve_reviewed_at` != '';
-- Create "new_ctx_conversations" table
CREATE TABLE `new_ctx_conversations` (
  `id` integer NULL PRIMARY KEY AUTOINCREMENT,
  `session_id` text NOT NULL,
  `title` text NULL,
  `channel` text NOT NULL DEFAULT '',
  `archived` integer NOT NULL DEFAULT 0,
  `last_active` text NOT NULL DEFAULT (datetime('now')),
  `bootstrapped_at` text NULL,
  `agent_id` text NULL,
  `user_id` integer NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now'))
);
-- Copy rows from old table "ctx_conversations" to new temporary table "new_ctx_conversations"
INSERT INTO `new_ctx_conversations` (`id`, `session_id`, `title`, `channel`, `archived`, `last_active`, `bootstrapped_at`, `agent_id`, `user_id`, `created_at`, `updated_at`) SELECT `id`, `session_id`, `title`, `channel`, `archived`, `last_active`, `bootstrapped_at`, `agent_id`, `user_id`, `created_at`, `updated_at` FROM `ctx_conversations`;
-- Drop "ctx_conversations" table after copying rows
DROP TABLE `ctx_conversations`;
-- Rename temporary table "new_ctx_conversations" to "ctx_conversations"
ALTER TABLE `new_ctx_conversations` RENAME TO `ctx_conversations`;
-- Create index "ctx_conversations_session_id" to table: "ctx_conversations"
CREATE UNIQUE INDEX `ctx_conversations_session_id` ON `ctx_conversations` (`session_id`);
-- Enable back the enforcement of foreign-keys constraints
PRAGMA foreign_keys = on;
