-- Disable the enforcement of foreign-keys constraints
PRAGMA foreign_keys = off;
-- Create "new_auth_user_agents" table
CREATE TABLE `new_auth_user_agents` (
  `user_id` text NOT NULL,
  `agent_id` text NOT NULL,
  PRIMARY KEY (`user_id`, `agent_id`),
  CONSTRAINT `0` FOREIGN KEY (`agent_id`) REFERENCES `settings_agents` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `1` FOREIGN KEY (`user_id`) REFERENCES `auth_users` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Copy rows from old table "auth_user_agents" to new temporary table "new_auth_user_agents"
INSERT INTO `new_auth_user_agents` (`user_id`, `agent_id`) SELECT `user_id`, `agent_id` FROM `auth_user_agents`;
-- Drop "auth_user_agents" table after copying rows
DROP TABLE `auth_user_agents`;
-- Rename temporary table "new_auth_user_agents" to "auth_user_agents"
ALTER TABLE `new_auth_user_agents` RENAME TO `auth_user_agents`;
-- Create "new_auth_sessions" table
CREATE TABLE `new_auth_sessions` (
  `id` text NULL,
  `user_id` text NOT NULL,
  `expires_at` text NOT NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`user_id`) REFERENCES `auth_users` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Copy rows from old table "auth_sessions" to new temporary table "new_auth_sessions"
INSERT INTO `new_auth_sessions` (`id`, `user_id`, `expires_at`, `created_at`) SELECT `id`, `user_id`, `expires_at`, `created_at` FROM `auth_sessions`;
-- Drop "auth_sessions" table after copying rows
DROP TABLE `auth_sessions`;
-- Rename temporary table "new_auth_sessions" to "auth_sessions"
ALTER TABLE `new_auth_sessions` RENAME TO `auth_sessions`;
-- Create "new_ctx_agent_memory" table
CREATE TABLE `new_ctx_agent_memory` (
  `user_id` text NOT NULL,
  `agent_id` text NOT NULL,
  `content` text NOT NULL DEFAULT '',
  `soul` text NOT NULL DEFAULT '',
  `version` integer NOT NULL DEFAULT 0,
  `constraints` text NOT NULL DEFAULT '[]',
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`user_id`, `agent_id`),
  CONSTRAINT `0` FOREIGN KEY (`agent_id`) REFERENCES `settings_agents` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Copy rows from old table "ctx_agent_memory" to new temporary table "new_ctx_agent_memory"
INSERT INTO `new_ctx_agent_memory` (`user_id`, `agent_id`, `content`, `soul`, `version`, `constraints`, `updated_at`) SELECT `user_id`, `agent_id`, `content`, `soul`, `version`, `constraints`, `updated_at` FROM `ctx_agent_memory`;
-- Drop "ctx_agent_memory" table after copying rows
DROP TABLE `ctx_agent_memory`;
-- Rename temporary table "new_ctx_agent_memory" to "ctx_agent_memory"
ALTER TABLE `new_ctx_agent_memory` RENAME TO `ctx_agent_memory`;
-- Create "new_sched_jobs" table
CREATE TABLE `new_sched_jobs` (
  `id` text NULL,
  `owner_kind` text NOT NULL DEFAULT 'user',
  `exec_scope` text NOT NULL DEFAULT 'user',
  `plugin_id` text NOT NULL DEFAULT '',
  `job_key` text NOT NULL DEFAULT '',
  `runtime_name` text NOT NULL DEFAULT '',
  `name` text NOT NULL,
  `description` text NOT NULL DEFAULT '',
  `schedule_cron` text NOT NULL DEFAULT '',
  `schedule_every` text NOT NULL DEFAULT '',
  `schedule_at` text NOT NULL DEFAULT '',
  `message` text NOT NULL DEFAULT '',
  `payload` text NOT NULL DEFAULT '{}',
  `session_mode` text NOT NULL DEFAULT 'reuse',
  `enabled` integer NOT NULL DEFAULT 1,
  `agent_id` text NULL,
  `user_id` text NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  `last_run_at` text NULL,
  `last_error` text NOT NULL DEFAULT '',
  PRIMARY KEY (`id`)
);
-- Copy rows from old table "sched_jobs" to new temporary table "new_sched_jobs"
INSERT INTO `new_sched_jobs` (`id`, `owner_kind`, `exec_scope`, `plugin_id`, `job_key`, `runtime_name`, `name`, `description`, `schedule_cron`, `schedule_every`, `schedule_at`, `message`, `payload`, `session_mode`, `enabled`, `agent_id`, `user_id`, `created_at`, `updated_at`, `last_run_at`, `last_error`) SELECT `id`, `owner_kind`, `exec_scope`, `plugin_id`, `job_key`, `runtime_name`, `name`, `description`, `schedule_cron`, `schedule_every`, `schedule_at`, `message`, `payload`, `session_mode`, `enabled`, `agent_id`, `user_id`, `created_at`, `updated_at`, `last_run_at`, `last_error` FROM `sched_jobs`;
-- Drop "sched_jobs" table after copying rows
DROP TABLE `sched_jobs`;
-- Rename temporary table "new_sched_jobs" to "sched_jobs"
ALTER TABLE `new_sched_jobs` RENAME TO `sched_jobs`;
-- Create index "idx_sched_jobs_owner" to table: "sched_jobs"
CREATE INDEX `idx_sched_jobs_owner` ON `sched_jobs` (`owner_kind`, `plugin_id`, `job_key`);
-- Create "new_skills" table
CREATE TABLE `new_skills` (
  `id` text NULL,
  `scope` text NOT NULL,
  `user_id` text NULL,
  `agent_id` text NULL,
  `name` text NOT NULL,
  `description` text NOT NULL,
  `status` text NOT NULL DEFAULT 'active',
  `disable_model_invocation` integer NOT NULL DEFAULT 0,
  `metadata` text NOT NULL DEFAULT '{}',
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`agent_id`) REFERENCES `settings_agents` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `1` FOREIGN KEY (`user_id`) REFERENCES `auth_users` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CHECK (scope IN ('system','agent','user')),
  CHECK (status IN ('draft','active','deprecated')),
  CHECK (
        (scope='system'  AND user_id IS NULL     AND agent_id IS NULL) OR
        (scope='agent'   AND user_id IS NULL     AND agent_id IS NOT NULL) OR
        (scope='user'    AND user_id IS NOT NULL AND agent_id IS NULL)
    )
);
-- Copy rows from old table "skills" to new temporary table "new_skills"
INSERT INTO `new_skills` (`id`, `scope`, `user_id`, `agent_id`, `name`, `description`, `status`, `disable_model_invocation`, `metadata`, `created_at`, `updated_at`) SELECT `id`, `scope`, `user_id`, `agent_id`, `name`, `description`, `status`, `disable_model_invocation`, `metadata`, `created_at`, `updated_at` FROM `skills`;
-- Drop "skills" table after copying rows
DROP TABLE `skills`;
-- Rename temporary table "new_skills" to "skills"
ALTER TABLE `new_skills` RENAME TO `skills`;
-- Create index "idx_skills_owner_name" to table: "skills"
CREATE UNIQUE INDEX `idx_skills_owner_name` ON `skills` (`name`, `scope`, (ifnull(user_id, 0)), (ifnull(agent_id, '')));
-- Create index "idx_skills_visibility" to table: "skills"
CREATE INDEX `idx_skills_visibility` ON `skills` (`scope`, `user_id`, `agent_id`);
-- Create "new_memory_snapshots" table
CREATE TABLE `new_memory_snapshots` (
  `session_id` text NOT NULL,
  `user_id` text NOT NULL,
  `agent_id` text NOT NULL,
  `version` integer NOT NULL DEFAULT 0,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`session_id`, `user_id`, `agent_id`)
);
-- Copy rows from old table "memory_snapshots" to new temporary table "new_memory_snapshots"
INSERT INTO `new_memory_snapshots` (`session_id`, `user_id`, `agent_id`, `version`, `created_at`, `updated_at`) SELECT `session_id`, `user_id`, `agent_id`, `version`, `created_at`, `updated_at` FROM `memory_snapshots`;
-- Drop "memory_snapshots" table after copying rows
DROP TABLE `memory_snapshots`;
-- Rename temporary table "new_memory_snapshots" to "memory_snapshots"
ALTER TABLE `new_memory_snapshots` RENAME TO `memory_snapshots`;
-- Create index "idx_memory_snapshots_user_agent" to table: "memory_snapshots"
CREATE INDEX `idx_memory_snapshots_user_agent` ON `memory_snapshots` (`user_id`, `agent_id`);
-- Create "new_rss_feeds" table
CREATE TABLE `new_rss_feeds` (
  `id` text NULL,
  `user_id` text NOT NULL,
  `agent_id` text NULL,
  `url` text NOT NULL,
  `title` text NOT NULL DEFAULT '',
  `description` text NOT NULL DEFAULT '',
  `check_interval` text NOT NULL DEFAULT '1h',
  `last_checked_at` text NULL,
  `last_etag` text NOT NULL DEFAULT '',
  `last_modified` text NOT NULL DEFAULT '',
  `enabled` integer NOT NULL DEFAULT 1,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`agent_id`) REFERENCES `settings_agents` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `1` FOREIGN KEY (`user_id`) REFERENCES `auth_users` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Copy rows from old table "rss_feeds" to new temporary table "new_rss_feeds"
INSERT INTO `new_rss_feeds` (`id`, `user_id`, `agent_id`, `url`, `title`, `description`, `check_interval`, `last_checked_at`, `last_etag`, `last_modified`, `enabled`, `created_at`, `updated_at`) SELECT `id`, `user_id`, `agent_id`, `url`, `title`, `description`, `check_interval`, `last_checked_at`, `last_etag`, `last_modified`, `enabled`, `created_at`, `updated_at` FROM `rss_feeds`;
-- Drop "rss_feeds" table after copying rows
DROP TABLE `rss_feeds`;
-- Rename temporary table "new_rss_feeds" to "rss_feeds"
ALTER TABLE `new_rss_feeds` RENAME TO `rss_feeds`;
-- Create index "idx_rss_feeds_user_url" to table: "rss_feeds"
CREATE UNIQUE INDEX `idx_rss_feeds_user_url` ON `rss_feeds` (`user_id`, `url`);
-- Create "new_articles" table
CREATE TABLE `new_articles` (
  `id` text NOT NULL,
  `user_id` text NOT NULL,
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
-- Create "new_sched_job_runs" table
CREATE TABLE `new_sched_job_runs` (
  `id` text NOT NULL,
  `job_id` text NOT NULL,
  `session_id` text NOT NULL DEFAULT '',
  `status` text NOT NULL DEFAULT 'running',
  `started_at` text NOT NULL DEFAULT (datetime('now')),
  `finished_at` text NULL,
  `error` text NOT NULL DEFAULT '',
  `user_id` text NULL,
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`job_id`) REFERENCES `sched_jobs` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Copy rows from old table "sched_job_runs" to new temporary table "new_sched_job_runs"
INSERT INTO `new_sched_job_runs` (`id`, `job_id`, `session_id`, `status`, `started_at`, `finished_at`, `error`, `user_id`) SELECT `id`, `job_id`, `session_id`, `status`, `started_at`, `finished_at`, `error`, `user_id` FROM `sched_job_runs`;
-- Drop "sched_job_runs" table after copying rows
DROP TABLE `sched_job_runs`;
-- Rename temporary table "new_sched_job_runs" to "sched_job_runs"
ALTER TABLE `new_sched_job_runs` RENAME TO `sched_job_runs`;
-- Create index "idx_sched_job_runs_job_id" to table: "sched_job_runs"
CREATE INDEX `idx_sched_job_runs_job_id` ON `sched_job_runs` (`job_id`, `started_at` DESC);
-- Create "new_recally_digests" table
CREATE TABLE `new_recally_digests` (
  `id` text NOT NULL,
  `user_id` text NOT NULL,
  `date` text NOT NULL,
  `narrative` text NOT NULL DEFAULT '',
  `saved_yesterday_count` integer NOT NULL DEFAULT 0,
  `unread_count` integer NOT NULL DEFAULT 0,
  `read_count` integer NOT NULL DEFAULT 0,
  `archived_count` integer NOT NULL DEFAULT 0,
  `starred_count` integer NOT NULL DEFAULT 0,
  `worth_revisiting_count` integer NOT NULL DEFAULT 0,
  `total_articles` integer NOT NULL DEFAULT 0,
  `top_tags` text NOT NULL DEFAULT '[]',
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`user_id`) REFERENCES `auth_users` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Copy rows from old table "recally_digests" to new temporary table "new_recally_digests"
INSERT INTO `new_recally_digests` (`id`, `user_id`, `date`, `narrative`, `saved_yesterday_count`, `unread_count`, `read_count`, `archived_count`, `starred_count`, `worth_revisiting_count`, `total_articles`, `top_tags`, `created_at`, `updated_at`) SELECT `id`, `user_id`, `date`, `narrative`, `saved_yesterday_count`, `unread_count`, `read_count`, `archived_count`, `starred_count`, `worth_revisiting_count`, `total_articles`, `top_tags`, `created_at`, `updated_at` FROM `recally_digests`;
-- Drop "recally_digests" table after copying rows
DROP TABLE `recally_digests`;
-- Rename temporary table "new_recally_digests" to "recally_digests"
ALTER TABLE `new_recally_digests` RENAME TO `recally_digests`;
-- Create index "idx_recally_digests_user_date" to table: "recally_digests"
CREATE UNIQUE INDEX `idx_recally_digests_user_date` ON `recally_digests` (`user_id`, `date`);
-- Create index "idx_recally_digests_user_id" to table: "recally_digests"
CREATE INDEX `idx_recally_digests_user_id` ON `recally_digests` (`user_id`);
-- Create "new_agent_task" table
CREATE TABLE `new_agent_task` (
  `id` text NOT NULL,
  `title` text NOT NULL,
  `description` text NOT NULL DEFAULT '',
  `status` text NOT NULL DEFAULT 'pending',
  `priority` text NOT NULL DEFAULT 'routine',
  `session_id` text NULL,
  `context` text NOT NULL DEFAULT '{}',
  `review_request` text NOT NULL DEFAULT '{}',
  `deps` text NOT NULL DEFAULT '[]',
  `notify_at` text NULL,
  `scheduler_job_id` text NULL,
  `scheduler_run_id` text NULL,
  `agent_id` text NULL,
  `user_id` text NOT NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`user_id`) REFERENCES `auth_users` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `1` FOREIGN KEY (`agent_id`) REFERENCES `settings_agents` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `2` FOREIGN KEY (`scheduler_run_id`) REFERENCES `sched_job_runs` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `3` FOREIGN KEY (`scheduler_job_id`) REFERENCES `sched_jobs` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CHECK (status IN ('pending','running','blocked','review_requested','done','failed','cancelled')),
  CHECK (priority IN ('routine','urgent'))
);
-- Copy rows from old table "agent_task" to new temporary table "new_agent_task"
INSERT INTO `new_agent_task` (`id`, `title`, `description`, `status`, `priority`, `session_id`, `context`, `review_request`, `deps`, `notify_at`, `scheduler_job_id`, `scheduler_run_id`, `agent_id`, `user_id`, `created_at`, `updated_at`) SELECT `id`, `title`, `description`, `status`, `priority`, `session_id`, `context`, `review_request`, `deps`, `notify_at`, `scheduler_job_id`, `scheduler_run_id`, `agent_id`, `user_id`, `created_at`, `updated_at` FROM `agent_task`;
-- Drop "agent_task" table after copying rows
DROP TABLE `agent_task`;
-- Rename temporary table "new_agent_task" to "agent_task"
ALTER TABLE `new_agent_task` RENAME TO `agent_task`;
-- Create index "idx_agent_task_user_id_status" to table: "agent_task"
CREATE INDEX `idx_agent_task_user_id_status` ON `agent_task` (`user_id`, `status`);
-- Create index "idx_agent_task_status" to table: "agent_task"
CREATE INDEX `idx_agent_task_status` ON `agent_task` (`status`);
-- Create index "idx_agent_task_session_id" to table: "agent_task"
CREATE INDEX `idx_agent_task_session_id` ON `agent_task` (`session_id`);
-- Create index "idx_agent_task_scheduler_job_id" to table: "agent_task"
CREATE INDEX `idx_agent_task_scheduler_job_id` ON `agent_task` (`scheduler_job_id`);
-- Create index "idx_agent_task_scheduler_run_id" to table: "agent_task"
CREATE INDEX `idx_agent_task_scheduler_run_id` ON `agent_task` (`scheduler_run_id`);
-- Create index "idx_agent_task_agent_id" to table: "agent_task"
CREATE INDEX `idx_agent_task_agent_id` ON `agent_task` (`agent_id`);
-- Create "new_auth_identities" table
CREATE TABLE `new_auth_identities` (
  `id` text NULL,
  `user_id` text NOT NULL,
  `platform` text NOT NULL,
  `external_id` text NOT NULL,
  `name` text NOT NULL DEFAULT '',
  `linked_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`user_id`) REFERENCES `auth_users` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Copy rows from old table "auth_identities" to new temporary table "new_auth_identities"
INSERT INTO `new_auth_identities` (`id`, `user_id`, `platform`, `external_id`, `name`, `linked_at`) SELECT `id`, `user_id`, `platform`, `external_id`, `name`, `linked_at` FROM `auth_identities`;
-- Drop "auth_identities" table after copying rows
DROP TABLE `auth_identities`;
-- Rename temporary table "new_auth_identities" to "auth_identities"
ALTER TABLE `new_auth_identities` RENAME TO `auth_identities`;
-- Create index "auth_identities_platform_external_id" to table: "auth_identities"
CREATE UNIQUE INDEX `auth_identities_platform_external_id` ON `auth_identities` (`platform`, `external_id`);
-- Create "new_auth_users" table
CREATE TABLE `new_auth_users` (
  `id` text NULL,
  `username` text NOT NULL,
  `password_hash` text NOT NULL DEFAULT '',
  `role` text NOT NULL DEFAULT 'user',
  `is_active` integer NOT NULL DEFAULT 1,
  `default_agent_id` text NULL,
  `notify_identity_id` text NULL,
  `age_public_key` text NOT NULL DEFAULT '',
  `age_private_key` text NOT NULL DEFAULT '',
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`notify_identity_id`) REFERENCES `auth_identities` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `1` FOREIGN KEY (`default_agent_id`) REFERENCES `settings_agents` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Copy rows from old table "auth_users" to new temporary table "new_auth_users"
INSERT INTO `new_auth_users` (`id`, `username`, `password_hash`, `role`, `is_active`, `default_agent_id`, `notify_identity_id`, `age_public_key`, `age_private_key`, `created_at`, `updated_at`) SELECT `id`, `username`, `password_hash`, `role`, `is_active`, `default_agent_id`, `notify_identity_id`, `age_public_key`, `age_private_key`, `created_at`, `updated_at` FROM `auth_users`;
-- Drop "auth_users" table after copying rows
DROP TABLE `auth_users`;
-- Rename temporary table "new_auth_users" to "auth_users"
ALTER TABLE `new_auth_users` RENAME TO `auth_users`;
-- Create index "auth_users_username" to table: "auth_users"
CREATE UNIQUE INDEX `auth_users_username` ON `auth_users` (`username`);
-- Create "new_ctx_conversations" table
CREATE TABLE `new_ctx_conversations` (
  `id` text NULL,
  `session_id` text NOT NULL,
  `title` text NULL,
  `channel` text NOT NULL DEFAULT '',
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
INSERT INTO `new_ctx_conversations` (`id`, `session_id`, `title`, `channel`, `archived`, `last_active`, `bootstrapped_at`, `agent_id`, `user_id`, `created_at`, `updated_at`) SELECT `id`, `session_id`, `title`, `channel`, `archived`, `last_active`, `bootstrapped_at`, `agent_id`, `user_id`, `created_at`, `updated_at` FROM `ctx_conversations`;
-- Drop "ctx_conversations" table after copying rows
DROP TABLE `ctx_conversations`;
-- Rename temporary table "new_ctx_conversations" to "ctx_conversations"
ALTER TABLE `new_ctx_conversations` RENAME TO `ctx_conversations`;
-- Create index "ctx_conversations_session_id" to table: "ctx_conversations"
CREATE UNIQUE INDEX `ctx_conversations_session_id` ON `ctx_conversations` (`session_id`);
-- Create "new_vault_entries" table
CREATE TABLE `new_vault_entries` (
  `id` text NULL,
  `user_id` text NOT NULL,
  `name` text NOT NULL,
  `ciphertext` text NOT NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`user_id`) REFERENCES `auth_users` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Copy rows from old table "vault_entries" to new temporary table "new_vault_entries"
INSERT INTO `new_vault_entries` (`id`, `user_id`, `name`, `ciphertext`, `created_at`, `updated_at`) SELECT `id`, `user_id`, `name`, `ciphertext`, `created_at`, `updated_at` FROM `vault_entries`;
-- Drop "vault_entries" table after copying rows
DROP TABLE `vault_entries`;
-- Rename temporary table "new_vault_entries" to "vault_entries"
ALTER TABLE `new_vault_entries` RENAME TO `vault_entries`;
-- Create index "vault_entries_user_id_name" to table: "vault_entries"
CREATE UNIQUE INDEX `vault_entries_user_id_name` ON `vault_entries` (`user_id`, `name`);
-- Create "new_memory_changelog" table
CREATE TABLE `new_memory_changelog` (
  `id` text NULL,
  `user_id` text NOT NULL,
  `agent_id` text NOT NULL,
  `session_id` text NULL,
  `entity_id` text NULL,
  `scope` text NOT NULL,
  `action` text NOT NULL,
  `source` text NOT NULL,
  `memory_version_before` integer NULL,
  `memory_version_after` integer NULL,
  `before_text` text NULL,
  `after_text` text NULL,
  `metadata` text NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CHECK (scope IN ('profile', 'soul', 'constraint', 'skill', 'compaction')),
  CHECK (action IN ('create', 'update', 'delete', 'compact')),
  CHECK (source IN ('user', 'agent', 'reflect', 'system'))
);
-- Copy rows from old table "memory_changelog" to new temporary table "new_memory_changelog"
INSERT INTO `new_memory_changelog` (`id`, `user_id`, `agent_id`, `session_id`, `entity_id`, `scope`, `action`, `source`, `memory_version_before`, `memory_version_after`, `before_text`, `after_text`, `metadata`, `created_at`) SELECT `id`, `user_id`, `agent_id`, `session_id`, `entity_id`, `scope`, `action`, `source`, `memory_version_before`, `memory_version_after`, `before_text`, `after_text`, `metadata`, `created_at` FROM `memory_changelog`;
-- Drop "memory_changelog" table after copying rows
DROP TABLE `memory_changelog`;
-- Rename temporary table "new_memory_changelog" to "memory_changelog"
ALTER TABLE `new_memory_changelog` RENAME TO `memory_changelog`;
-- Create index "idx_memory_changelog_user_agent" to table: "memory_changelog"
CREATE INDEX `idx_memory_changelog_user_agent` ON `memory_changelog` (`user_id`, `agent_id`, `scope`);
-- Create index "idx_memory_changelog_version" to table: "memory_changelog"
CREATE INDEX `idx_memory_changelog_version` ON `memory_changelog` (`user_id`, `agent_id`, `scope`, `memory_version_after`);
-- Create index "idx_memory_changelog_session" to table: "memory_changelog"
CREATE INDEX `idx_memory_changelog_session` ON `memory_changelog` (`session_id`);
-- Create index "idx_memory_changelog_created" to table: "memory_changelog"
CREATE INDEX `idx_memory_changelog_created` ON `memory_changelog` (`created_at`);
-- Create "new_auth_user_tokens" table
CREATE TABLE `new_auth_user_tokens` (
  `id` text NULL,
  `user_id` text NOT NULL,
  `name` text NOT NULL DEFAULT '',
  `token_hash` text NOT NULL,
  `token_prefix` text NOT NULL DEFAULT '',
  `auto_generated` integer NOT NULL DEFAULT 0,
  `last_used_at` text NULL,
  `expires_at` text NULL,
  `rotated_at` text NULL,
  `revoked_at` text NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`user_id`) REFERENCES `auth_users` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Copy rows from old table "auth_user_tokens" to new temporary table "new_auth_user_tokens"
INSERT INTO `new_auth_user_tokens` (`id`, `user_id`, `name`, `token_hash`, `token_prefix`, `auto_generated`, `last_used_at`, `expires_at`, `rotated_at`, `revoked_at`, `created_at`, `updated_at`) SELECT `id`, `user_id`, `name`, `token_hash`, `token_prefix`, `auto_generated`, `last_used_at`, `expires_at`, `rotated_at`, `revoked_at`, `created_at`, `updated_at` FROM `auth_user_tokens`;
-- Drop "auth_user_tokens" table after copying rows
DROP TABLE `auth_user_tokens`;
-- Rename temporary table "new_auth_user_tokens" to "auth_user_tokens"
ALTER TABLE `new_auth_user_tokens` RENAME TO `auth_user_tokens`;
-- Create index "auth_user_tokens_token_hash" to table: "auth_user_tokens"
CREATE UNIQUE INDEX `auth_user_tokens_token_hash` ON `auth_user_tokens` (`token_hash`);
-- Create index "idx_auth_user_tokens_auto_active" to table: "auth_user_tokens"
CREATE UNIQUE INDEX `idx_auth_user_tokens_auto_active` ON `auth_user_tokens` (`user_id`) WHERE auto_generated = 1 AND revoked_at IS NULL;
-- Enable back the enforcement of foreign-keys constraints
PRAGMA foreign_keys = on;
