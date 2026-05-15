-- Disable the enforcement of foreign-keys constraints
PRAGMA foreign_keys = off;
-- Create "new_ctx_messages" table
CREATE TABLE `new_ctx_messages` (
  `id` text NULL,
  `conversation_id` text NOT NULL,
  `seq` integer NOT NULL,
  `role` text NOT NULL,
  `event_type` text NOT NULL DEFAULT 'text',
  `content` text NOT NULL,
  `token_count` integer NOT NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`conversation_id`) REFERENCES `ctx_conversations` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CHECK (role IN ('user', 'assistant', 'tool'))
);
-- Copy rows from old table "ctx_messages" to new temporary table "new_ctx_messages"
INSERT INTO `new_ctx_messages` (`id`, `conversation_id`, `seq`, `role`, `event_type`, `content`, `token_count`, `created_at`) SELECT `id`, `conversation_id`, `seq`, `role`, `event_type`, `content`, `token_count`, `created_at` FROM `ctx_messages`;
-- Drop "ctx_messages" table after copying rows
DROP TABLE `ctx_messages`;
-- Rename temporary table "new_ctx_messages" to "ctx_messages"
ALTER TABLE `new_ctx_messages` RENAME TO `ctx_messages`;
-- Create index "ctx_messages_conversation_id_seq" to table: "ctx_messages"
CREATE UNIQUE INDEX `ctx_messages_conversation_id_seq` ON `ctx_messages` (`conversation_id`, `seq`);
-- Create index "idx_ctx_messages_conv_seq" to table: "ctx_messages"
CREATE INDEX `idx_ctx_messages_conv_seq` ON `ctx_messages` (`conversation_id`, `seq`);
-- Create "new_ctx_message_parts" table
CREATE TABLE `new_ctx_message_parts` (
  `id` text NULL,
  `message_id` text NOT NULL,
  `part_type` text NOT NULL,
  `ordinal` integer NOT NULL,
  `text_content` text NULL,
  `tool_call_id` text NULL,
  `tool_name` text NULL,
  `tool_input` text NULL,
  `tool_output` text NULL,
  `metadata` text NULL,
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`message_id`) REFERENCES `ctx_messages` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CHECK (part_type IN ('text', 'reasoning', 'tool'))
);
-- Copy rows from old table "ctx_message_parts" to new temporary table "new_ctx_message_parts"
INSERT INTO `new_ctx_message_parts` (`id`, `message_id`, `part_type`, `ordinal`, `text_content`, `tool_call_id`, `tool_name`, `tool_input`, `tool_output`, `metadata`) SELECT `id`, `message_id`, `part_type`, `ordinal`, `text_content`, `tool_call_id`, `tool_name`, `tool_input`, `tool_output`, `metadata` FROM `ctx_message_parts`;
-- Drop "ctx_message_parts" table after copying rows
DROP TABLE `ctx_message_parts`;
-- Rename temporary table "new_ctx_message_parts" to "ctx_message_parts"
ALTER TABLE `new_ctx_message_parts` RENAME TO `ctx_message_parts`;
-- Create index "ctx_message_parts_message_id_ordinal" to table: "ctx_message_parts"
CREATE UNIQUE INDEX `ctx_message_parts_message_id_ordinal` ON `ctx_message_parts` (`message_id`, `ordinal`);
-- Create "new_ctx_summaries" table
CREATE TABLE `new_ctx_summaries` (
  `id` text NULL,
  `conversation_id` text NOT NULL,
  `kind` text NOT NULL,
  `depth` integer NOT NULL DEFAULT 0,
  `content` text NOT NULL,
  `token_count` integer NOT NULL,
  `earliest_at` text NULL,
  `latest_at` text NULL,
  `descendant_count` integer NOT NULL DEFAULT 0,
  `descendant_token_count` integer NOT NULL DEFAULT 0,
  `source_message_token_count` integer NOT NULL DEFAULT 0,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`conversation_id`) REFERENCES `ctx_conversations` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CHECK (kind IN ('leaf', 'condensed'))
);
-- Copy rows from old table "ctx_summaries" to new temporary table "new_ctx_summaries"
INSERT INTO `new_ctx_summaries` (`id`, `conversation_id`, `kind`, `depth`, `content`, `token_count`, `earliest_at`, `latest_at`, `descendant_count`, `descendant_token_count`, `source_message_token_count`, `created_at`) SELECT `id`, `conversation_id`, `kind`, `depth`, `content`, `token_count`, `earliest_at`, `latest_at`, `descendant_count`, `descendant_token_count`, `source_message_token_count`, `created_at` FROM `ctx_summaries`;
-- Drop "ctx_summaries" table after copying rows
DROP TABLE `ctx_summaries`;
-- Rename temporary table "new_ctx_summaries" to "ctx_summaries"
ALTER TABLE `new_ctx_summaries` RENAME TO `ctx_summaries`;
-- Create index "idx_ctx_summaries_conv" to table: "ctx_summaries"
CREATE INDEX `idx_ctx_summaries_conv` ON `ctx_summaries` (`conversation_id`, `created_at`);
-- Create "new_ctx_summary_messages" table
CREATE TABLE `new_ctx_summary_messages` (
  `summary_id` text NOT NULL,
  `message_id` text NOT NULL,
  `ordinal` integer NOT NULL,
  PRIMARY KEY (`summary_id`, `message_id`),
  CONSTRAINT `0` FOREIGN KEY (`message_id`) REFERENCES `ctx_messages` (`id`) ON UPDATE NO ACTION ON DELETE RESTRICT,
  CONSTRAINT `1` FOREIGN KEY (`summary_id`) REFERENCES `ctx_summaries` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Copy rows from old table "ctx_summary_messages" to new temporary table "new_ctx_summary_messages"
INSERT INTO `new_ctx_summary_messages` (`summary_id`, `message_id`, `ordinal`) SELECT `summary_id`, `message_id`, `ordinal` FROM `ctx_summary_messages`;
-- Drop "ctx_summary_messages" table after copying rows
DROP TABLE `ctx_summary_messages`;
-- Rename temporary table "new_ctx_summary_messages" to "ctx_summary_messages"
ALTER TABLE `new_ctx_summary_messages` RENAME TO `ctx_summary_messages`;
-- Create "new_ctx_items" table
CREATE TABLE `new_ctx_items` (
  `conversation_id` text NOT NULL,
  `ordinal` integer NOT NULL,
  `item_type` text NOT NULL,
  `message_id` text NULL,
  `summary_id` text NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`conversation_id`, `ordinal`),
  CONSTRAINT `0` FOREIGN KEY (`summary_id`) REFERENCES `ctx_summaries` (`id`) ON UPDATE NO ACTION ON DELETE RESTRICT,
  CONSTRAINT `1` FOREIGN KEY (`message_id`) REFERENCES `ctx_messages` (`id`) ON UPDATE NO ACTION ON DELETE RESTRICT,
  CONSTRAINT `2` FOREIGN KEY (`conversation_id`) REFERENCES `ctx_conversations` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CHECK (item_type IN ('message', 'summary')),
  CHECK (
        (item_type = 'message' AND message_id IS NOT NULL AND summary_id IS NULL) OR
        (item_type = 'summary' AND summary_id IS NOT NULL AND message_id IS NULL)
    )
);
-- Copy rows from old table "ctx_items" to new temporary table "new_ctx_items"
INSERT INTO `new_ctx_items` (`conversation_id`, `ordinal`, `item_type`, `message_id`, `summary_id`, `created_at`) SELECT `conversation_id`, `ordinal`, `item_type`, `message_id`, `summary_id`, `created_at` FROM `ctx_items`;
-- Drop "ctx_items" table after copying rows
DROP TABLE `ctx_items`;
-- Rename temporary table "new_ctx_items" to "ctx_items"
ALTER TABLE `new_ctx_items` RENAME TO `ctx_items`;
-- Create index "idx_ctx_items_conv" to table: "ctx_items"
CREATE INDEX `idx_ctx_items_conv` ON `ctx_items` (`conversation_id`, `ordinal`);
-- Create "new_auth_identities" table
CREATE TABLE `new_auth_identities` (
  `id` text NULL,
  `user_id` integer NOT NULL,
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
  `id` integer NULL PRIMARY KEY AUTOINCREMENT,
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
  `user_id` integer NULL,
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
  `user_id` integer NOT NULL,
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
  `user_id` integer NOT NULL,
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
  `user_id` integer NOT NULL,
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
-- Create "new_auth_oauth_provider" table
CREATE TABLE `new_auth_oauth_provider` (
  `id` text NULL,
  `provider_id` text NOT NULL,
  `client_id` text NOT NULL DEFAULT '',
  `client_secret_enc` text NOT NULL DEFAULT '',
  `redirect_url` text NOT NULL DEFAULT '',
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`)
);
-- Copy rows from old table "auth_oauth_provider" to new temporary table "new_auth_oauth_provider"
INSERT INTO `new_auth_oauth_provider` (`id`, `provider_id`, `client_id`, `client_secret_enc`, `redirect_url`, `created_at`, `updated_at`) SELECT `id`, `provider_id`, `client_id`, `client_secret_enc`, `redirect_url`, `created_at`, `updated_at` FROM `auth_oauth_provider`;
-- Drop "auth_oauth_provider" table after copying rows
DROP TABLE `auth_oauth_provider`;
-- Rename temporary table "new_auth_oauth_provider" to "auth_oauth_provider"
ALTER TABLE `new_auth_oauth_provider` RENAME TO `auth_oauth_provider`;
-- Create index "auth_oauth_provider_provider_id" to table: "auth_oauth_provider"
CREATE UNIQUE INDEX `auth_oauth_provider_provider_id` ON `auth_oauth_provider` (`provider_id`);
-- Enable back the enforcement of foreign-keys constraints
PRAGMA foreign_keys = on;
