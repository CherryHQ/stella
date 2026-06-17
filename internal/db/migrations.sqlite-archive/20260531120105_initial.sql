-- Create "app_setting" table
CREATE TABLE `app_setting` (
  `key` text NOT NULL,
  `value` text NOT NULL DEFAULT '{}',
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`key`)
);
-- Create "agent" table
CREATE TABLE `agent` (
  `id` text NULL,
  `name` text NOT NULL,
  `model` text NOT NULL DEFAULT '',
  `model_strong` text NOT NULL DEFAULT '',
  `model_fast` text NOT NULL DEFAULT '',
  `system_prompt` text NOT NULL DEFAULT '',
  `soul` text NOT NULL DEFAULT '',
  `workspace` text NOT NULL,
  `sandbox` text NOT NULL DEFAULT '{}',
  `enabled_builtin_skills` text NOT NULL DEFAULT '[]',
  `scope` text NOT NULL DEFAULT 'system',
  `creator_id` text NOT NULL DEFAULT '',
  `enabled` integer NOT NULL DEFAULT 1,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`)
);
-- Create "channel" table
CREATE TABLE `channel` (
  `id` text NOT NULL,
  `type` text NOT NULL DEFAULT '',
  `agent_id` text NULL,
  `enabled` integer NOT NULL DEFAULT 1,
  `config` text NOT NULL DEFAULT '{}',
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`agent_id`) REFERENCES `agent` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL
);
-- Create "plugin" table
CREATE TABLE `plugin` (
  `id` text NOT NULL,
  `kind` text NOT NULL,
  `name` text NOT NULL,
  `enabled` integer NOT NULL DEFAULT 1,
  `config` text NOT NULL DEFAULT '{}',
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`)
);
-- Create "provider" table
CREATE TABLE `provider` (
  `id` text NULL,
  `type` text NOT NULL,
  `name` text NOT NULL,
  `enabled` integer NOT NULL DEFAULT 1,
  `config` text NOT NULL DEFAULT '{}',
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`)
);
-- Create "channel_agent" table
CREATE TABLE `channel_agent` (
  `channel_id` text NOT NULL DEFAULT '',
  `platform` text NOT NULL,
  `chat_id` text NOT NULL,
  `agent_id` text NOT NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`channel_id`, `platform`, `chat_id`),
  CONSTRAINT `0` FOREIGN KEY (`agent_id`) REFERENCES `agent` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create "ctx_agent_memory" table
CREATE TABLE `ctx_agent_memory` (
  `user_id` text NOT NULL,
  `agent_id` text NOT NULL,
  `content` text NOT NULL DEFAULT '',
  `soul` text NOT NULL DEFAULT '',
  `version` integer NOT NULL DEFAULT 0,
  `constraints` text NOT NULL DEFAULT '[]',
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`user_id`, `agent_id`),
  CONSTRAINT `0` FOREIGN KEY (`agent_id`) REFERENCES `agent` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `1` FOREIGN KEY (`user_id`) REFERENCES `auth_user` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create "ctx_agent_memory_changelog" table
CREATE TABLE `ctx_agent_memory_changelog` (
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
  CONSTRAINT `0` FOREIGN KEY (`agent_id`) REFERENCES `agent` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `1` FOREIGN KEY (`user_id`) REFERENCES `auth_user` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "idx_ctx_agent_memory_changelog_user_agent" to table: "ctx_agent_memory_changelog"
CREATE INDEX `idx_ctx_agent_memory_changelog_user_agent` ON `ctx_agent_memory_changelog` (`user_id`, `agent_id`, `scope`);
-- Create index "idx_ctx_agent_memory_changelog_version" to table: "ctx_agent_memory_changelog"
CREATE INDEX `idx_ctx_agent_memory_changelog_version` ON `ctx_agent_memory_changelog` (`user_id`, `agent_id`, `scope`, `memory_version_after`);
-- Create index "idx_ctx_agent_memory_changelog_session" to table: "ctx_agent_memory_changelog"
CREATE INDEX `idx_ctx_agent_memory_changelog_session` ON `ctx_agent_memory_changelog` (`session_id`);
-- Create index "idx_ctx_agent_memory_changelog_created" to table: "ctx_agent_memory_changelog"
CREATE INDEX `idx_ctx_agent_memory_changelog_created` ON `ctx_agent_memory_changelog` (`created_at`);
-- Create "ctx_agent_memory_snapshot" table
CREATE TABLE `ctx_agent_memory_snapshot` (
  `session_id` text NOT NULL,
  `user_id` text NOT NULL,
  `agent_id` text NOT NULL,
  `version` integer NOT NULL DEFAULT 0,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`session_id`, `user_id`, `agent_id`),
  CONSTRAINT `0` FOREIGN KEY (`agent_id`) REFERENCES `agent` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `1` FOREIGN KEY (`user_id`) REFERENCES `auth_user` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "idx_ctx_agent_memory_snapshot_user_agent" to table: "ctx_agent_memory_snapshot"
CREATE INDEX `idx_ctx_agent_memory_snapshot_user_agent` ON `ctx_agent_memory_snapshot` (`user_id`, `agent_id`);
-- Create "ctx_conversation" table
CREATE TABLE `ctx_conversation` (
  `id` text NULL,
  `session_id` text NOT NULL,
  `title` text NULL,
  `channel` text NOT NULL DEFAULT '',
  `kind` text NOT NULL DEFAULT 'chat',
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
-- Create index "ctx_conversation_session_id" to table: "ctx_conversation"
CREATE UNIQUE INDEX `ctx_conversation_session_id` ON `ctx_conversation` (`session_id`);
-- Create index "idx_one_agent_main" to table: "ctx_conversation"
CREATE UNIQUE INDEX `idx_one_agent_main` ON `ctx_conversation` (`agent_id`, `user_id`) WHERE kind = 'main' AND project_id IS NULL AND archived = 0;
-- Create index "idx_one_project_main" to table: "ctx_conversation"
CREATE UNIQUE INDEX `idx_one_project_main` ON `ctx_conversation` (`project_id`) WHERE kind = 'main' AND project_id IS NOT NULL AND archived = 0;
-- Create "ctx_message" table
CREATE TABLE `ctx_message` (
  `id` text NULL,
  `conversation_id` text NOT NULL,
  `seq` integer NOT NULL,
  `role` text NOT NULL,
  `event_type` text NOT NULL DEFAULT 'text',
  `content` text NOT NULL,
  `token_count` integer NOT NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`conversation_id`) REFERENCES `ctx_conversation` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "ctx_message_conversation_id_seq" to table: "ctx_message"
CREATE UNIQUE INDEX `ctx_message_conversation_id_seq` ON `ctx_message` (`conversation_id`, `seq`);
-- Create index "idx_ctx_message_conv_seq" to table: "ctx_message"
CREATE INDEX `idx_ctx_message_conv_seq` ON `ctx_message` (`conversation_id`, `seq`);
-- Create "ctx_message_part" table
CREATE TABLE `ctx_message_part` (
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
  CONSTRAINT `0` FOREIGN KEY (`message_id`) REFERENCES `ctx_message` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "ctx_message_part_message_id_ordinal" to table: "ctx_message_part"
CREATE UNIQUE INDEX `ctx_message_part_message_id_ordinal` ON `ctx_message_part` (`message_id`, `ordinal`);
-- Create "ctx_summary" table
CREATE TABLE `ctx_summary` (
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
  CONSTRAINT `0` FOREIGN KEY (`conversation_id`) REFERENCES `ctx_conversation` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "idx_ctx_summary_conv" to table: "ctx_summary"
CREATE INDEX `idx_ctx_summary_conv` ON `ctx_summary` (`conversation_id`, `created_at`);
-- Create "ctx_summary_message" table
CREATE TABLE `ctx_summary_message` (
  `summary_id` text NOT NULL,
  `message_id` text NOT NULL,
  `ordinal` integer NOT NULL,
  PRIMARY KEY (`summary_id`, `message_id`),
  CONSTRAINT `0` FOREIGN KEY (`message_id`) REFERENCES `ctx_message` (`id`) ON UPDATE NO ACTION ON DELETE RESTRICT,
  CONSTRAINT `1` FOREIGN KEY (`summary_id`) REFERENCES `ctx_summary` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create "ctx_summary_parent" table
CREATE TABLE `ctx_summary_parent` (
  `summary_id` text NOT NULL,
  `parent_summary_id` text NOT NULL,
  `ordinal` integer NOT NULL,
  PRIMARY KEY (`summary_id`, `parent_summary_id`),
  CONSTRAINT `0` FOREIGN KEY (`parent_summary_id`) REFERENCES `ctx_summary` (`id`) ON UPDATE NO ACTION ON DELETE RESTRICT,
  CONSTRAINT `1` FOREIGN KEY (`summary_id`) REFERENCES `ctx_summary` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create "ctx_item" table
CREATE TABLE `ctx_item` (
  `conversation_id` text NOT NULL,
  `ordinal` integer NOT NULL,
  `item_type` text NOT NULL,
  `message_id` text NULL,
  `summary_id` text NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`conversation_id`, `ordinal`),
  CONSTRAINT `0` FOREIGN KEY (`summary_id`) REFERENCES `ctx_summary` (`id`) ON UPDATE NO ACTION ON DELETE RESTRICT,
  CONSTRAINT `1` FOREIGN KEY (`message_id`) REFERENCES `ctx_message` (`id`) ON UPDATE NO ACTION ON DELETE RESTRICT,
  CONSTRAINT `2` FOREIGN KEY (`conversation_id`) REFERENCES `ctx_conversation` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CHECK (
        (item_type = 'message' AND message_id IS NOT NULL AND summary_id IS NULL) OR
        (item_type = 'summary' AND summary_id IS NOT NULL AND message_id IS NULL)
    )
);
-- Create index "idx_ctx_item_conv" to table: "ctx_item"
CREATE INDEX `idx_ctx_item_conv` ON `ctx_item` (`conversation_id`, `ordinal`);
-- Create "sched_job" table
CREATE TABLE `sched_job` (
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
-- Create index "idx_sched_job_owner" to table: "sched_job"
CREATE INDEX `idx_sched_job_owner` ON `sched_job` (`owner_kind`, `plugin_id`, `job_key`);
-- Create "sched_job_run" table
CREATE TABLE `sched_job_run` (
  `id` text NOT NULL,
  `job_id` text NOT NULL,
  `session_id` text NOT NULL DEFAULT '',
  `status` text NOT NULL DEFAULT 'running',
  `started_at` text NOT NULL DEFAULT (datetime('now')),
  `finished_at` text NULL,
  `error` text NOT NULL DEFAULT '',
  `user_id` text NULL,
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`job_id`) REFERENCES `sched_job` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "idx_sched_job_run_job_id" to table: "sched_job_run"
CREATE INDEX `idx_sched_job_run_job_id` ON `sched_job_run` (`job_id`, `started_at` DESC);
-- Create "auth_policy" table
CREATE TABLE `auth_policy` (
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
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CHECK (effect IN ('allow', 'deny'))
);
-- Create "auth_user_agent" table
CREATE TABLE `auth_user_agent` (
  `user_id` text NOT NULL,
  `agent_id` text NOT NULL,
  PRIMARY KEY (`user_id`, `agent_id`),
  CONSTRAINT `0` FOREIGN KEY (`agent_id`) REFERENCES `agent` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `1` FOREIGN KEY (`user_id`) REFERENCES `auth_user` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create "auth_user_token" table
CREATE TABLE `auth_user_token` (
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
  CONSTRAINT `0` FOREIGN KEY (`user_id`) REFERENCES `auth_user` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "auth_user_token_token_hash" to table: "auth_user_token"
CREATE UNIQUE INDEX `auth_user_token_token_hash` ON `auth_user_token` (`token_hash`);
-- Create index "idx_auth_user_token_auto_active" to table: "auth_user_token"
CREATE UNIQUE INDEX `idx_auth_user_token_auto_active` ON `auth_user_token` (`user_id`) WHERE auto_generated = 1 AND revoked_at IS NULL;
-- Create "auth_user" table
CREATE TABLE `auth_user` (
  `id` text NOT NULL,
  `email` text NOT NULL,
  `name` text NOT NULL DEFAULT '',
  `avatar_url` text NOT NULL DEFAULT '',
  `role` text NOT NULL DEFAULT 'user',
  `is_active` integer NOT NULL DEFAULT 1,
  `default_agent_id` text NULL,
  `notify_identity_id` text NULL,
  `age_public_key` text NOT NULL DEFAULT '',
  `age_private_key` text NOT NULL DEFAULT '',
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`notify_identity_id`) REFERENCES `channel_identity` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `1` FOREIGN KEY (`default_agent_id`) REFERENCES `agent` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "auth_user_email" to table: "auth_user"
CREATE UNIQUE INDEX `auth_user_email` ON `auth_user` (`email`);
-- Create index "idx_auth_user_email" to table: "auth_user"
CREATE INDEX `idx_auth_user_email` ON `auth_user` (`email`);
-- Create "channel_identity" table
CREATE TABLE `channel_identity` (
  `id` text NOT NULL,
  `user_id` text NOT NULL,
  `platform` text NOT NULL,
  `external_id` text NOT NULL,
  `name` text NOT NULL DEFAULT '',
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`user_id`) REFERENCES `auth_user` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "channel_identity_platform_external_id" to table: "channel_identity"
CREATE UNIQUE INDEX `channel_identity_platform_external_id` ON `channel_identity` (`platform`, `external_id`);
-- Create index "idx_channel_identity_user_id" to table: "channel_identity"
CREATE INDEX `idx_channel_identity_user_id` ON `channel_identity` (`user_id`);
-- Create "auth_identity" table
CREATE TABLE `auth_identity` (
  `id` text NOT NULL,
  `user_id` text NOT NULL,
  `provider` text NOT NULL,
  `provider_subject` text NOT NULL,
  `email` text NOT NULL DEFAULT '',
  `name` text NOT NULL DEFAULT '',
  `avatar_url` text NOT NULL DEFAULT '',
  `raw_claims` text NOT NULL DEFAULT '{}',
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`user_id`) REFERENCES `auth_user` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "auth_identity_provider_provider_subject" to table: "auth_identity"
CREATE UNIQUE INDEX `auth_identity_provider_provider_subject` ON `auth_identity` (`provider`, `provider_subject`);
-- Create index "idx_auth_identity_user_id" to table: "auth_identity"
CREATE INDEX `idx_auth_identity_user_id` ON `auth_identity` (`user_id`);
-- Create "auth_session" table
CREATE TABLE `auth_session` (
  `id` text NOT NULL,
  `user_id` text NOT NULL,
  `token_hash` text NOT NULL,
  `expires_at` text NOT NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`user_id`) REFERENCES `auth_user` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "auth_session_token_hash" to table: "auth_session"
CREATE UNIQUE INDEX `auth_session_token_hash` ON `auth_session` (`token_hash`);
-- Create index "idx_auth_session_user_id" to table: "auth_session"
CREATE INDEX `idx_auth_session_user_id` ON `auth_session` (`user_id`);
-- Create index "idx_auth_session_token_hash" to table: "auth_session"
CREATE INDEX `idx_auth_session_token_hash` ON `auth_session` (`token_hash`);
-- Create "auth_credential" table
CREATE TABLE `auth_credential` (
  `id` text NOT NULL,
  `user_id` text NOT NULL,
  `password_hash` text NOT NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`user_id`) REFERENCES `auth_user` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "auth_credential_user_id" to table: "auth_credential"
CREATE UNIQUE INDEX `auth_credential_user_id` ON `auth_credential` (`user_id`);
-- Create index "idx_auth_credential_user_id" to table: "auth_credential"
CREATE INDEX `idx_auth_credential_user_id` ON `auth_credential` (`user_id`);
-- Create "auth_oidc_code" table
CREATE TABLE `auth_oidc_code` (
  `id` text NOT NULL,
  `code_hash` text NOT NULL,
  `user_id` text NOT NULL,
  `client_id` text NOT NULL,
  `redirect_uri` text NOT NULL,
  `scopes` text NOT NULL DEFAULT '[]',
  `nonce` text NOT NULL DEFAULT '',
  `pkce_challenge` text NOT NULL DEFAULT '',
  `pkce_method` text NOT NULL DEFAULT 'S256',
  `expires_at` text NOT NULL,
  `consumed_at` text NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`user_id`) REFERENCES `auth_user` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "auth_oidc_code_code_hash" to table: "auth_oidc_code"
CREATE UNIQUE INDEX `auth_oidc_code_code_hash` ON `auth_oidc_code` (`code_hash`);
-- Create index "idx_auth_oidc_code_code_hash" to table: "auth_oidc_code"
CREATE INDEX `idx_auth_oidc_code_code_hash` ON `auth_oidc_code` (`code_hash`);
-- Create index "idx_auth_oidc_code_user_id" to table: "auth_oidc_code"
CREATE INDEX `idx_auth_oidc_code_user_id` ON `auth_oidc_code` (`user_id`);
-- Create "auth_oidc_access_token" table
CREATE TABLE `auth_oidc_access_token` (
  `id` text NOT NULL,
  `token_hash` text NOT NULL,
  `user_id` text NOT NULL,
  `client_id` text NOT NULL,
  `scopes` text NOT NULL DEFAULT '[]',
  `expires_at` text NOT NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`user_id`) REFERENCES `auth_user` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "auth_oidc_access_token_token_hash" to table: "auth_oidc_access_token"
CREATE UNIQUE INDEX `auth_oidc_access_token_token_hash` ON `auth_oidc_access_token` (`token_hash`);
-- Create index "idx_auth_oidc_access_token_token_hash" to table: "auth_oidc_access_token"
CREATE INDEX `idx_auth_oidc_access_token_token_hash` ON `auth_oidc_access_token` (`token_hash`);
-- Create index "idx_auth_oidc_access_token_user_id" to table: "auth_oidc_access_token"
CREATE INDEX `idx_auth_oidc_access_token_user_id` ON `auth_oidc_access_token` (`user_id`);
-- Create "share" table
CREATE TABLE `share` (
  `id` text NULL,
  `token_hash` text NOT NULL,
  `user_id` text NOT NULL,
  `title` text NOT NULL,
  `media_type` text NOT NULL,
  `content` blob NOT NULL,
  `expires_at` text NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`user_id`) REFERENCES `auth_user` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "share_token_hash" to table: "share"
CREATE UNIQUE INDEX `share_token_hash` ON `share` (`token_hash`);
-- Create index "idx_share_user" to table: "share"
CREATE INDEX `idx_share_user` ON `share` (`user_id`, `created_at` DESC);
-- Create "plugin_state" table
CREATE TABLE `plugin_state` (
  `plugin_id` text NOT NULL,
  `scope_kind` text NOT NULL,
  `scope_id` text NOT NULL DEFAULT '',
  `state_key` text NOT NULL,
  `value` text NOT NULL DEFAULT '{}',
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`plugin_id`, `scope_kind`, `scope_id`, `state_key`)
);
-- Create "skill" table
CREATE TABLE `skill` (
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
  CONSTRAINT `0` FOREIGN KEY (`agent_id`) REFERENCES `agent` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `1` FOREIGN KEY (`user_id`) REFERENCES `auth_user` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CHECK (
        (scope='system'  AND user_id IS NULL     AND agent_id IS NULL) OR
        (scope='agent'   AND user_id IS NULL     AND agent_id IS NOT NULL) OR
        (scope='user'    AND user_id IS NOT NULL)
    )
);
-- Create index "idx_skill_owner_name" to table: "skill"
CREATE UNIQUE INDEX `idx_skill_owner_name` ON `skill` (`name`, `scope`, (ifnull(user_id, 0)), (ifnull(agent_id, '')));
-- Create index "idx_skill_visibility" to table: "skill"
CREATE INDEX `idx_skill_visibility` ON `skill` (`scope`, `user_id`, `agent_id`);
-- Create "skill_file" table
CREATE TABLE `skill_file` (
  `skill_id` text NOT NULL,
  `path` text NOT NULL,
  `content` text NOT NULL,
  PRIMARY KEY (`skill_id`, `path`),
  CONSTRAINT `0` FOREIGN KEY (`skill_id`) REFERENCES `skill` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create "vault_entry" table
CREATE TABLE `vault_entry` (
  `id` text NULL,
  `user_id` text NOT NULL,
  `name` text NOT NULL,
  `ciphertext` text NOT NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`user_id`) REFERENCES `auth_user` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "vault_entry_user_id_name" to table: "vault_entry"
CREATE UNIQUE INDEX `vault_entry_user_id_name` ON `vault_entry` (`user_id`, `name`);
-- Create "plugin_oauth_provider" table
CREATE TABLE `plugin_oauth_provider` (
  `id` text NULL,
  `provider_id` text NOT NULL,
  `client_id` text NOT NULL DEFAULT '',
  `client_secret_enc` text NOT NULL DEFAULT '',
  `redirect_url` text NOT NULL DEFAULT '',
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`)
);
-- Create index "plugin_oauth_provider_provider_id" to table: "plugin_oauth_provider"
CREATE UNIQUE INDEX `plugin_oauth_provider_provider_id` ON `plugin_oauth_provider` (`provider_id`);
-- Create "recally_article" table
CREATE TABLE `recally_article` (
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
  CONSTRAINT `0` FOREIGN KEY (`agent_id`) REFERENCES `agent` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `1` FOREIGN KEY (`user_id`) REFERENCES `auth_user` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "idx_recally_article_user_canonical" to table: "recally_article"
CREATE UNIQUE INDEX `idx_recally_article_user_canonical` ON `recally_article` (`user_id`, `canonical_url`);
-- Create index "idx_recally_article_user_status" to table: "recally_article"
CREATE INDEX `idx_recally_article_user_status` ON `recally_article` (`user_id`, `status`);
-- Create index "idx_recally_article_user_source" to table: "recally_article"
CREATE INDEX `idx_recally_article_user_source` ON `recally_article` (`user_id`, `source_type`);
-- Create index "idx_recally_article_user_starred" to table: "recally_article"
CREATE INDEX `idx_recally_article_user_starred` ON `recally_article` (`user_id`, `starred`) WHERE starred = 1;
-- Create index "idx_recally_article_saved_at" to table: "recally_article"
CREATE INDEX `idx_recally_article_saved_at` ON `recally_article` (`saved_at`);
-- Create "recally_rss_feed" table
CREATE TABLE `recally_rss_feed` (
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
  CONSTRAINT `0` FOREIGN KEY (`agent_id`) REFERENCES `agent` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `1` FOREIGN KEY (`user_id`) REFERENCES `auth_user` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "idx_recally_rss_feed_user_url" to table: "recally_rss_feed"
CREATE UNIQUE INDEX `idx_recally_rss_feed_user_url` ON `recally_rss_feed` (`user_id`, `url`);
-- Create "recally_rss_feed_entry" table
CREATE TABLE `recally_rss_feed_entry` (
  `id` text NULL,
  `feed_id` text NOT NULL,
  `guid` text NOT NULL,
  `url` text NOT NULL DEFAULT '',
  `title` text NOT NULL DEFAULT '',
  `status` text NOT NULL DEFAULT 'pending',
  `article_id` text NULL,
  `attempts` integer NOT NULL DEFAULT 0,
  `error_msg` text NOT NULL DEFAULT '',
  `discovered_at` text NOT NULL DEFAULT (datetime('now')),
  `processed_at` text NULL,
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`article_id`) REFERENCES `recally_article` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `1` FOREIGN KEY (`feed_id`) REFERENCES `recally_rss_feed` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "idx_recally_rss_feed_entry_feed_guid" to table: "recally_rss_feed_entry"
CREATE UNIQUE INDEX `idx_recally_rss_feed_entry_feed_guid` ON `recally_rss_feed_entry` (`feed_id`, `guid`);
-- Create index "idx_recally_rss_feed_entry_status" to table: "recally_rss_feed_entry"
CREATE INDEX `idx_recally_rss_feed_entry_status` ON `recally_rss_feed_entry` (`status`);
-- Create "recally_digest" table
CREATE TABLE `recally_digest` (
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
  CONSTRAINT `0` FOREIGN KEY (`user_id`) REFERENCES `auth_user` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "idx_recally_digest_user_date" to table: "recally_digest"
CREATE UNIQUE INDEX `idx_recally_digest_user_date` ON `recally_digest` (`user_id`, `date`);
-- Create index "idx_recally_digest_user_id" to table: "recally_digest"
CREATE INDEX `idx_recally_digest_user_id` ON `recally_digest` (`user_id`);
-- Create "recally_digest_article" table
CREATE TABLE `recally_digest_article` (
  `digest_id` text NOT NULL,
  `article_id` text NOT NULL,
  `section` text NOT NULL,
  `position` integer NOT NULL DEFAULT 0,
  PRIMARY KEY (`digest_id`, `article_id`, `section`),
  CONSTRAINT `0` FOREIGN KEY (`article_id`) REFERENCES `recally_article` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `1` FOREIGN KEY (`digest_id`) REFERENCES `recally_digest` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "idx_recally_digest_article_digest" to table: "recally_digest_article"
CREATE INDEX `idx_recally_digest_article_digest` ON `recally_digest_article` (`digest_id`);
-- Create "agent_goal" table
CREATE TABLE `agent_goal` (
  `id` text NOT NULL,
  `user_id` text NOT NULL,
  `agent_id` text NULL,
  `title` text NOT NULL,
  `description` text NOT NULL DEFAULT '',
  `status` text NOT NULL DEFAULT 'draft',
  `priority` text NOT NULL DEFAULT 'routine',
  `review_policy` text NOT NULL DEFAULT 'none',
  `active_review_id` text NULL,
  `context` text NOT NULL DEFAULT '{}',
  `output` text NOT NULL DEFAULT '{}',
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  `completed_at` text NULL,
  `cancelled_at` text NULL,
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`active_review_id`) REFERENCES `agent_review` (`id`) ON UPDATE NO ACTION ON DELETE RESTRICT,
  CONSTRAINT `1` FOREIGN KEY (`agent_id`) REFERENCES `agent` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `2` FOREIGN KEY (`user_id`) REFERENCES `auth_user` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create "agent_task_run" table
CREATE TABLE `agent_task_run` (
  `id` text NOT NULL,
  `task_id` text NULL,
  `goal_id` text NULL,
  `user_id` text NOT NULL,
  `agent_id` text NULL,
  `executor_agent_id` text NULL,
  `kind` text NOT NULL DEFAULT 'worker',
  `attempt_no` integer NOT NULL DEFAULT 1,
  `status` text NOT NULL DEFAULT 'queued',
  `session_id` text NOT NULL,
  `input` text NOT NULL DEFAULT '{}',
  `result` text NOT NULL DEFAULT '{}',
  `error` text NOT NULL DEFAULT '',
  `heartbeat_at` text NULL,
  `lease_expires_at` text NULL,
  `worker_id` text NOT NULL DEFAULT '',
  `started_at` text NULL,
  `finished_at` text NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`executor_agent_id`) REFERENCES `agent` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `1` FOREIGN KEY (`agent_id`) REFERENCES `agent` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `2` FOREIGN KEY (`user_id`) REFERENCES `auth_user` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `3` FOREIGN KEY (`goal_id`) REFERENCES `agent_goal` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `4` FOREIGN KEY (`task_id`) REFERENCES `agent_task` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CHECK (
      (task_id IS NOT NULL AND goal_id IS NULL     AND kind IN ('worker','reviewer'))
      OR
      (task_id IS NULL     AND goal_id IS NOT NULL AND kind IN ('planner','synthesizer'))
    )
);
-- Create index "idx_agent_task_run_task" to table: "agent_task_run"
CREATE INDEX `idx_agent_task_run_task` ON `agent_task_run` (`task_id`, `attempt_no` DESC);
-- Create index "idx_agent_task_run_active" to table: "agent_task_run"
CREATE INDEX `idx_agent_task_run_active` ON `agent_task_run` (`status`) WHERE status IN ('queued','running');
-- Create index "idx_agent_task_run_lease" to table: "agent_task_run"
CREATE INDEX `idx_agent_task_run_lease` ON `agent_task_run` (`lease_expires_at`) WHERE status IN ('queued','running');
-- Create index "uniq_active_worker_run" to table: "agent_task_run"
CREATE UNIQUE INDEX `uniq_active_worker_run` ON `agent_task_run` (`task_id`) WHERE task_id IS NOT NULL AND kind = 'worker' AND status IN ('queued','running');
-- Create index "uniq_active_reviewer_run" to table: "agent_task_run"
CREATE UNIQUE INDEX `uniq_active_reviewer_run` ON `agent_task_run` (`task_id`) WHERE task_id IS NOT NULL AND kind = 'reviewer' AND status IN ('queued','running');
-- Create index "uniq_active_planner_run" to table: "agent_task_run"
CREATE UNIQUE INDEX `uniq_active_planner_run` ON `agent_task_run` (`goal_id`) WHERE goal_id IS NOT NULL AND kind = 'planner' AND status IN ('queued','running');
-- Create index "uniq_active_synthesizer_run" to table: "agent_task_run"
CREATE UNIQUE INDEX `uniq_active_synthesizer_run` ON `agent_task_run` (`goal_id`) WHERE goal_id IS NOT NULL AND kind = 'synthesizer' AND status IN ('queued','running');
-- Create index "idx_agent_task_run_goal" to table: "agent_task_run"
CREATE INDEX `idx_agent_task_run_goal` ON `agent_task_run` (`goal_id`);
-- Create index "uniq_goal_run_attempt" to table: "agent_task_run"
CREATE UNIQUE INDEX `uniq_goal_run_attempt` ON `agent_task_run` (`goal_id`, `kind`, `attempt_no`) WHERE goal_id IS NOT NULL;
-- Create index "uniq_task_run_attempt" to table: "agent_task_run"
CREATE UNIQUE INDEX `uniq_task_run_attempt` ON `agent_task_run` (`task_id`, `kind`, `attempt_no`) WHERE task_id IS NOT NULL;
-- Create "agent_task_blocker" table
CREATE TABLE `agent_task_blocker` (
  `id` text NOT NULL,
  `task_id` text NOT NULL,
  `kind` text NOT NULL,
  `status` text NOT NULL DEFAULT 'open',
  `question` text NOT NULL DEFAULT '',
  `detail` text NOT NULL DEFAULT '{}',
  `resolution` text NOT NULL DEFAULT '{}',
  `created_by_run_id` text NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `resolved_at` text NULL,
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`created_by_run_id`) REFERENCES `agent_task_run` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `1` FOREIGN KEY (`task_id`) REFERENCES `agent_task` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "idx_agent_task_blocker_task_open" to table: "agent_task_blocker"
CREATE INDEX `idx_agent_task_blocker_task_open` ON `agent_task_blocker` (`task_id`) WHERE status='open';
-- Create index "uniq_open_blocker_per_task" to table: "agent_task_blocker"
CREATE UNIQUE INDEX `uniq_open_blocker_per_task` ON `agent_task_blocker` (`task_id`) WHERE status = 'open';
-- Create "agent_task_criterion" table
CREATE TABLE `agent_task_criterion` (
  `id` text NOT NULL,
  `task_id` text NOT NULL,
  `description` text NOT NULL,
  `required_flag` integer NOT NULL DEFAULT 1,
  `position` integer NOT NULL DEFAULT 0,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`task_id`) REFERENCES `agent_task` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "idx_agent_task_criterion_task" to table: "agent_task_criterion"
CREATE INDEX `idx_agent_task_criterion_task` ON `agent_task_criterion` (`task_id`, `position`);
-- Create "agent_review" table
CREATE TABLE `agent_review` (
  `id` text NOT NULL,
  `task_id` text NULL,
  `goal_id` text NULL,
  `submitted_run_id` text NULL,
  `reviewer_run_id` text NULL,
  `reviewer_type` text NOT NULL,
  `reviewer_user_id` text NULL,
  `escalated_from_review_id` text NULL,
  `status` text NOT NULL DEFAULT 'requested',
  `summary` text NOT NULL DEFAULT '',
  `feedback` text NOT NULL DEFAULT '',
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  `resolved_at` text NULL,
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`escalated_from_review_id`) REFERENCES `agent_review` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `1` FOREIGN KEY (`reviewer_user_id`) REFERENCES `auth_user` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `2` FOREIGN KEY (`reviewer_run_id`) REFERENCES `agent_task_run` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `3` FOREIGN KEY (`submitted_run_id`) REFERENCES `agent_task_run` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `4` FOREIGN KEY (`goal_id`) REFERENCES `agent_goal` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `5` FOREIGN KEY (`task_id`) REFERENCES `agent_task` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CHECK (
      (task_id IS NOT NULL AND goal_id IS NULL)
      OR
      (task_id IS NULL AND goal_id IS NOT NULL)
    )
);
-- Create index "idx_agent_review_task" to table: "agent_review"
CREATE INDEX `idx_agent_review_task` ON `agent_review` (`task_id`, `created_at` DESC);
-- Create index "idx_agent_review_open" to table: "agent_review"
CREATE INDEX `idx_agent_review_open` ON `agent_review` (`status`) WHERE status IN ('requested','in_progress');
-- Create index "uniq_open_review_per_task" to table: "agent_review"
CREATE UNIQUE INDEX `uniq_open_review_per_task` ON `agent_review` (`task_id`) WHERE task_id IS NOT NULL AND status IN ('requested','in_progress');
-- Create index "uniq_open_review_per_goal" to table: "agent_review"
CREATE UNIQUE INDEX `uniq_open_review_per_goal` ON `agent_review` (`goal_id`) WHERE goal_id IS NOT NULL AND status IN ('requested','in_progress');
-- Create "agent_review_item" table
CREATE TABLE `agent_review_item` (
  `id` text NOT NULL,
  `review_id` text NOT NULL,
  `criterion_id` text NULL,
  `passed` integer NULL,
  `evidence` text NOT NULL DEFAULT '',
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`criterion_id`) REFERENCES `agent_task_criterion` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `1` FOREIGN KEY (`review_id`) REFERENCES `agent_review` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "idx_agent_review_item_review" to table: "agent_review_item"
CREATE INDEX `idx_agent_review_item_review` ON `agent_review_item` (`review_id`);
-- Create "agent_task" table
CREATE TABLE `agent_task` (
  `id` text NOT NULL,
  `user_id` text NOT NULL,
  `agent_id` text NULL,
  `goal_id` text NULL,
  `title` text NOT NULL,
  `description` text NOT NULL DEFAULT '',
  `status` text NOT NULL DEFAULT 'draft',
  `priority` text NOT NULL DEFAULT 'routine',
  `review_policy` text NOT NULL DEFAULT 'none',
  `active_review_id` text NULL,
  `required` integer NOT NULL DEFAULT 1,
  `retry_count` integer NOT NULL DEFAULT 0,
  `max_retries` integer NOT NULL DEFAULT 3,
  `not_before` text NULL,
  `deadline_at` text NULL,
  `session_id` text NULL,
  `active_run_id` text NULL,
  `active_blocker_id` text NULL,
  `context` text NOT NULL DEFAULT '{}',
  `output` text NOT NULL DEFAULT '{}',
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  `completed_at` text NULL,
  `cancelled_at` text NULL,
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`active_blocker_id`) REFERENCES `agent_task_blocker` (`id`) ON UPDATE NO ACTION ON DELETE RESTRICT,
  CONSTRAINT `1` FOREIGN KEY (`active_run_id`) REFERENCES `agent_task_run` (`id`) ON UPDATE NO ACTION ON DELETE RESTRICT,
  CONSTRAINT `2` FOREIGN KEY (`active_review_id`) REFERENCES `agent_review` (`id`) ON UPDATE NO ACTION ON DELETE RESTRICT,
  CONSTRAINT `3` FOREIGN KEY (`goal_id`) REFERENCES `agent_goal` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `4` FOREIGN KEY (`agent_id`) REFERENCES `agent` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `5` FOREIGN KEY (`user_id`) REFERENCES `auth_user` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "idx_agent_task_status_not_before" to table: "agent_task"
CREATE INDEX `idx_agent_task_status_not_before` ON `agent_task` (`status`, `not_before`);
-- Create index "idx_agent_task_session" to table: "agent_task"
CREATE INDEX `idx_agent_task_session` ON `agent_task` (`session_id`);
-- Create index "idx_agent_task_goal" to table: "agent_task"
CREATE INDEX `idx_agent_task_goal` ON `agent_task` (`goal_id`);
-- Create "agent_task_dep" table
CREATE TABLE `agent_task_dep` (
  `task_id` text NOT NULL,
  `dep_task_id` text NOT NULL,
  `dep_kind` text NOT NULL DEFAULT 'hard',
  `on_failure` text NOT NULL DEFAULT 'block',
  `waived_at` text NULL,
  `waived_by_user` text NULL,
  `waiver_reason` text NOT NULL DEFAULT '',
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`task_id`, `dep_task_id`),
  CONSTRAINT `0` FOREIGN KEY (`waived_by_user`) REFERENCES `auth_user` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `1` FOREIGN KEY (`dep_task_id`) REFERENCES `agent_task` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `2` FOREIGN KEY (`task_id`) REFERENCES `agent_task` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CHECK (task_id != dep_task_id)
);
-- Create index "idx_agent_task_dep_dep" to table: "agent_task_dep"
CREATE INDEX `idx_agent_task_dep_dep` ON `agent_task_dep` (`dep_task_id`);
-- Create "agent_task_dispatch_hint" table
CREATE TABLE `agent_task_dispatch_hint` (
  `id` text NOT NULL,
  `task_id` text NULL,
  `goal_id` text NULL,
  `kind` text NOT NULL,
  `executor_agent_id` text NOT NULL,
  `consumed_at` text NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`executor_agent_id`) REFERENCES `agent` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `1` FOREIGN KEY (`goal_id`) REFERENCES `agent_goal` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `2` FOREIGN KEY (`task_id`) REFERENCES `agent_task` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CHECK (
      (task_id IS NOT NULL AND goal_id IS NULL     AND kind IN ('worker','reviewer'))
      OR
      (task_id IS NULL     AND goal_id IS NOT NULL AND kind IN ('planner','synthesizer'))
    )
);
-- Create index "uniq_active_dispatch_hint_task" to table: "agent_task_dispatch_hint"
CREATE UNIQUE INDEX `uniq_active_dispatch_hint_task` ON `agent_task_dispatch_hint` (`task_id`, `kind`) WHERE task_id IS NOT NULL AND consumed_at IS NULL;
-- Create index "uniq_active_dispatch_hint_goal" to table: "agent_task_dispatch_hint"
CREATE UNIQUE INDEX `uniq_active_dispatch_hint_goal` ON `agent_task_dispatch_hint` (`goal_id`, `kind`) WHERE goal_id IS NOT NULL AND consumed_at IS NULL;
-- Create "agent_task_event" table
CREATE TABLE `agent_task_event` (
  `id` text NOT NULL,
  `task_id` text NULL,
  `goal_id` text NULL,
  `run_id` text NULL,
  `blocker_id` text NULL,
  `review_id` text NULL,
  `event_type` text NOT NULL,
  `from_status` text NULL,
  `to_status` text NULL,
  `actor_type` text NOT NULL,
  `actor_id` text NULL,
  `detail` text NOT NULL DEFAULT '{}',
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`review_id`) REFERENCES `agent_review` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `1` FOREIGN KEY (`blocker_id`) REFERENCES `agent_task_blocker` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `2` FOREIGN KEY (`run_id`) REFERENCES `agent_task_run` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `3` FOREIGN KEY (`goal_id`) REFERENCES `agent_goal` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `4` FOREIGN KEY (`task_id`) REFERENCES `agent_task` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "idx_agent_task_event_task" to table: "agent_task_event"
CREATE INDEX `idx_agent_task_event_task` ON `agent_task_event` (`task_id`, `created_at` DESC);
-- Create index "idx_agent_task_event_goal" to table: "agent_task_event"
CREATE INDEX `idx_agent_task_event_goal` ON `agent_task_event` (`goal_id`, `created_at` DESC);
-- Create index "idx_agent_task_event_run" to table: "agent_task_event"
CREATE INDEX `idx_agent_task_event_run` ON `agent_task_event` (`run_id`);
-- Create "project" table
CREATE TABLE `project` (
  `id` text NULL,
  `agent_id` text NOT NULL,
  `user_id` text NOT NULL,
  `name` text NOT NULL,
  `base_dir` text NOT NULL,
  `description` text NULL,
  `archived` integer NOT NULL DEFAULT 0,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`user_id`) REFERENCES `auth_user` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `1` FOREIGN KEY (`agent_id`) REFERENCES `agent` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "project_agent_id_user_id_name" to table: "project"
CREATE UNIQUE INDEX `project_agent_id_user_id_name` ON `project` (`agent_id`, `user_id`, `name`);
-- Create "plugin_override" table
CREATE TABLE `plugin_override` (
  `plugin_id` text NOT NULL,
  `enabled` integer NULL,
  `session_env_vault_key` text NOT NULL DEFAULT '',
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`plugin_id`)
);
