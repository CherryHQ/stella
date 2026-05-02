-- Disable the enforcement of foreign-keys constraints
PRAGMA foreign_keys = off;
-- Create "new_articles" table
CREATE TABLE `new_articles` (
  `id` text NOT NULL,
  `user_id` integer NOT NULL,
  `agent_id` text NULL,
  `url` text NOT NULL,
  `canonical_url` text NOT NULL,
  `source_type` text NOT NULL DEFAULT 'web',
  `title` text NOT NULL DEFAULT '',
  `author` text NOT NULL DEFAULT '',
  `summary` text NOT NULL DEFAULT '',
  `tags` text NOT NULL DEFAULT '[]',
  `status` text NOT NULL DEFAULT 'unread',
  `starred` integer NOT NULL DEFAULT 0,
  `file_path` text NOT NULL DEFAULT '',
  `metadata` text NOT NULL DEFAULT '{}',
  `published_at` text NULL,
  `saved_at` text NOT NULL DEFAULT (datetime('now')),
  `read_at` text NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`agent_id`) REFERENCES `settings_agents` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `1` FOREIGN KEY (`user_id`) REFERENCES `auth_users` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CHECK (source_type IN ('web','twitter','youtube','github','rss','pdf')),
  CHECK (status IN ('unread','read','archived'))
);
-- Copy rows from old table "articles" to new temporary table "new_articles"
INSERT INTO `new_articles` (`id`, `user_id`, `agent_id`, `url`, `canonical_url`, `source_type`, `title`, `author`, `summary`, `tags`, `status`, `starred`, `file_path`, `metadata`, `published_at`, `saved_at`, `read_at`, `created_at`, `updated_at`) SELECT `id`, `user_id`, `agent_id`, `url`, `canonical_url`, `source_type`, `title`, `author`, `summary`, `tags`, `status`, `starred`, `file_path`, `metadata`, `published_at`, `saved_at`, `read_at`, `created_at`, `updated_at` FROM `articles`;
-- Drop "articles" table after copying rows
DROP TABLE `articles`;
-- Rename temporary table "new_articles" to "articles"
ALTER TABLE `new_articles` RENAME TO `articles`;
-- Create index "idx_articles_user_canonical" to table: "articles"
CREATE UNIQUE INDEX `idx_articles_user_canonical` ON `articles` (`user_id`, `canonical_url`);
-- Create index "idx_articles_user_status" to table: "articles"
CREATE INDEX `idx_articles_user_status` ON `articles` (`user_id`, `status`);
-- Create index "idx_articles_user_source" to table: "articles"
CREATE INDEX `idx_articles_user_source` ON `articles` (`user_id`, `source_type`);
-- Create index "idx_articles_user_starred" to table: "articles"
CREATE INDEX `idx_articles_user_starred` ON `articles` (`user_id`, `starred`) WHERE starred = 1;
-- Create index "idx_articles_saved_at" to table: "articles"
CREATE INDEX `idx_articles_saved_at` ON `articles` (`saved_at`);
-- Add column "agent_id" to table: "auth_user_tokens"
ALTER TABLE `auth_user_tokens` ADD COLUMN `agent_id` text NULL;
-- Drop index "idx_auth_user_tokens_auto_active" from table: "auth_user_tokens"
DROP INDEX `idx_auth_user_tokens_auto_active`;
-- Create index "idx_auth_user_tokens_auto_active_user" to table: "auth_user_tokens"
CREATE UNIQUE INDEX `idx_auth_user_tokens_auto_active_user` ON `auth_user_tokens` (`user_id`) WHERE auto_generated = 1 AND revoked_at IS NULL AND agent_id IS NULL;
-- Create index "idx_auth_user_tokens_auto_active_agent" to table: "auth_user_tokens"
CREATE UNIQUE INDEX `idx_auth_user_tokens_auto_active_agent` ON `auth_user_tokens` (`user_id`, `agent_id`) WHERE auto_generated = 1 AND revoked_at IS NULL AND agent_id IS NOT NULL;
-- Enable back the enforcement of foreign-keys constraints
PRAGMA foreign_keys = on;
