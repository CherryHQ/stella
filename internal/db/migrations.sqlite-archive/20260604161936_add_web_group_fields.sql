-- Disable the enforcement of foreign-keys constraints
PRAGMA foreign_keys = off;
-- Create "new_ctx_group_state" table
CREATE TABLE `new_ctx_group_state` (
  `id` text NULL,
  `platform` text NOT NULL,
  `platform_group_id` text NOT NULL,
  `platform_thread_id` text NOT NULL DEFAULT '',
  `next_seq` integer NOT NULL DEFAULT 0,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  `group_name` text NOT NULL DEFAULT '',
  `created_by_user_id` text NULL,
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`created_by_user_id`) REFERENCES `auth_user` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Copy rows from old table "ctx_group_state" to new temporary table "new_ctx_group_state"
INSERT INTO `new_ctx_group_state` (`id`, `platform`, `platform_group_id`, `platform_thread_id`, `next_seq`, `created_at`, `updated_at`) SELECT `id`, `platform`, `platform_group_id`, `platform_thread_id`, `next_seq`, `created_at`, `updated_at` FROM `ctx_group_state`;
-- Drop "ctx_group_state" table after copying rows
DROP TABLE `ctx_group_state`;
-- Rename temporary table "new_ctx_group_state" to "ctx_group_state"
ALTER TABLE `new_ctx_group_state` RENAME TO `ctx_group_state`;
-- Create index "ctx_group_state_platform_platform_group_id_platform_thread_id" to table: "ctx_group_state"
CREATE UNIQUE INDEX `ctx_group_state_platform_platform_group_id_platform_thread_id` ON `ctx_group_state` (`platform`, `platform_group_id`, `platform_thread_id`);
-- Create index "idx_ctx_group_state_web_owner" to table: "ctx_group_state"
CREATE INDEX `idx_ctx_group_state_web_owner` ON `ctx_group_state` (`created_by_user_id`) WHERE platform = 'web';
-- Enable back the enforcement of foreign-keys constraints
PRAGMA foreign_keys = on;
