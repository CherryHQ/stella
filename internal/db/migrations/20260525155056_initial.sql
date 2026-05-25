-- Create "auth_organization" table
CREATE TABLE `auth_organization` (
  `id` text NOT NULL,
  `name` text NOT NULL,
  `external_id` text NOT NULL DEFAULT '',
  `source` text NOT NULL DEFAULT 'local',
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`)
);
-- Create index "auth_organization_source_external_id" to table: "auth_organization"
CREATE UNIQUE INDEX `auth_organization_source_external_id` ON `auth_organization` (`source`, `external_id`);
-- Create "settings" table
CREATE TABLE `settings` (
  `key` text NULL,
  `value` text NOT NULL DEFAULT '{}',
  `org_id` text NOT NULL,
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`key`),
  CONSTRAINT `0` FOREIGN KEY (`org_id`) REFERENCES `auth_organization` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "idx_settings_org_id" to table: "settings"
CREATE INDEX `idx_settings_org_id` ON `settings` (`org_id`);
-- Create "settings_agents" table
CREATE TABLE `settings_agents` (
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
  `org_id` text NOT NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`org_id`) REFERENCES `auth_organization` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "idx_settings_agents_org_id" to table: "settings_agents"
CREATE INDEX `idx_settings_agents_org_id` ON `settings_agents` (`org_id`);
-- Create "settings_channels" table
CREATE TABLE `settings_channels` (
  `id` text NULL,
  `type` text NOT NULL DEFAULT '',
  `agent_id` text NULL,
  `enabled` integer NOT NULL DEFAULT 1,
  `config` text NOT NULL DEFAULT '{}',
  `org_id` text NOT NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`org_id`) REFERENCES `auth_organization` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `1` FOREIGN KEY (`agent_id`) REFERENCES `settings_agents` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL
);
-- Create index "idx_settings_channels_org_id" to table: "settings_channels"
CREATE INDEX `idx_settings_channels_org_id` ON `settings_channels` (`org_id`);
-- Create "settings_plugins" table
CREATE TABLE `settings_plugins` (
  `id` text NULL,
  `kind` text NOT NULL,
  `name` text NOT NULL,
  `enabled` integer NOT NULL DEFAULT 1,
  `config` text NOT NULL DEFAULT '{}',
  `org_id` text NOT NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`org_id`) REFERENCES `auth_organization` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "idx_settings_plugins_org_id" to table: "settings_plugins"
CREATE INDEX `idx_settings_plugins_org_id` ON `settings_plugins` (`org_id`);
-- Create "settings_providers" table
CREATE TABLE `settings_providers` (
  `id` text NULL,
  `type` text NOT NULL,
  `name` text NOT NULL,
  `enabled` integer NOT NULL DEFAULT 1,
  `config` text NOT NULL DEFAULT '{}',
  `org_id` text NOT NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`org_id`) REFERENCES `auth_organization` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "idx_settings_providers_org_id" to table: "settings_providers"
CREATE INDEX `idx_settings_providers_org_id` ON `settings_providers` (`org_id`);
-- Create "settings_channel_agents" table
CREATE TABLE `settings_channel_agents` (
  `channel_id` text NOT NULL DEFAULT '',
  `platform` text NOT NULL,
  `chat_id` text NOT NULL,
  `agent_id` text NOT NULL,
  `org_id` text NOT NULL,
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`channel_id`, `platform`, `chat_id`),
  CONSTRAINT `0` FOREIGN KEY (`org_id`) REFERENCES `auth_organization` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `1` FOREIGN KEY (`agent_id`) REFERENCES `settings_agents` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "idx_settings_channel_agents_org_id" to table: "settings_channel_agents"
CREATE INDEX `idx_settings_channel_agents_org_id` ON `settings_channel_agents` (`org_id`);
-- Create "ctx_agent_memory" table
CREATE TABLE `ctx_agent_memory` (
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
-- Create "memory_changelog" table
CREATE TABLE `memory_changelog` (
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
-- Create index "idx_memory_changelog_user_agent" to table: "memory_changelog"
CREATE INDEX `idx_memory_changelog_user_agent` ON `memory_changelog` (`user_id`, `agent_id`, `scope`);
-- Create index "idx_memory_changelog_version" to table: "memory_changelog"
CREATE INDEX `idx_memory_changelog_version` ON `memory_changelog` (`user_id`, `agent_id`, `scope`, `memory_version_after`);
-- Create index "idx_memory_changelog_session" to table: "memory_changelog"
CREATE INDEX `idx_memory_changelog_session` ON `memory_changelog` (`session_id`);
-- Create index "idx_memory_changelog_created" to table: "memory_changelog"
CREATE INDEX `idx_memory_changelog_created` ON `memory_changelog` (`created_at`);
-- Create "memory_snapshots" table
CREATE TABLE `memory_snapshots` (
  `session_id` text NOT NULL,
  `user_id` text NOT NULL,
  `agent_id` text NOT NULL,
  `version` integer NOT NULL DEFAULT 0,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`session_id`, `user_id`, `agent_id`)
);
-- Create index "idx_memory_snapshots_user_agent" to table: "memory_snapshots"
CREATE INDEX `idx_memory_snapshots_user_agent` ON `memory_snapshots` (`user_id`, `agent_id`);
-- Create "ctx_conversations" table
CREATE TABLE `ctx_conversations` (
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
  `org_id` text NOT NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`org_id`) REFERENCES `auth_organization` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "ctx_conversations_session_id" to table: "ctx_conversations"
CREATE UNIQUE INDEX `ctx_conversations_session_id` ON `ctx_conversations` (`session_id`);
-- Create index "idx_ctx_conversations_org_id" to table: "ctx_conversations"
CREATE INDEX `idx_ctx_conversations_org_id` ON `ctx_conversations` (`org_id`);
-- Create index "idx_one_agent_main" to table: "ctx_conversations"
CREATE UNIQUE INDEX `idx_one_agent_main` ON `ctx_conversations` (`agent_id`, `user_id`) WHERE kind = 'main' AND project_id IS NULL AND archived = 0;
-- Create index "idx_one_project_main" to table: "ctx_conversations"
CREATE UNIQUE INDEX `idx_one_project_main` ON `ctx_conversations` (`project_id`) WHERE kind = 'main' AND project_id IS NOT NULL AND archived = 0;
-- Create "ctx_messages" table
CREATE TABLE `ctx_messages` (
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
-- Create index "ctx_messages_conversation_id_seq" to table: "ctx_messages"
CREATE UNIQUE INDEX `ctx_messages_conversation_id_seq` ON `ctx_messages` (`conversation_id`, `seq`);
-- Create index "idx_ctx_messages_conv_seq" to table: "ctx_messages"
CREATE INDEX `idx_ctx_messages_conv_seq` ON `ctx_messages` (`conversation_id`, `seq`);
-- Create "ctx_message_parts" table
CREATE TABLE `ctx_message_parts` (
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
-- Create index "ctx_message_parts_message_id_ordinal" to table: "ctx_message_parts"
CREATE UNIQUE INDEX `ctx_message_parts_message_id_ordinal` ON `ctx_message_parts` (`message_id`, `ordinal`);
-- Create "ctx_summaries" table
CREATE TABLE `ctx_summaries` (
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
-- Create index "idx_ctx_summaries_conv" to table: "ctx_summaries"
CREATE INDEX `idx_ctx_summaries_conv` ON `ctx_summaries` (`conversation_id`, `created_at`);
-- Create "ctx_summary_messages" table
CREATE TABLE `ctx_summary_messages` (
  `summary_id` text NOT NULL,
  `message_id` text NOT NULL,
  `ordinal` integer NOT NULL,
  PRIMARY KEY (`summary_id`, `message_id`),
  CONSTRAINT `0` FOREIGN KEY (`message_id`) REFERENCES `ctx_messages` (`id`) ON UPDATE NO ACTION ON DELETE RESTRICT,
  CONSTRAINT `1` FOREIGN KEY (`summary_id`) REFERENCES `ctx_summaries` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create "ctx_summary_parents" table
CREATE TABLE `ctx_summary_parents` (
  `summary_id` text NOT NULL,
  `parent_summary_id` text NOT NULL,
  `ordinal` integer NOT NULL,
  PRIMARY KEY (`summary_id`, `parent_summary_id`),
  CONSTRAINT `0` FOREIGN KEY (`parent_summary_id`) REFERENCES `ctx_summaries` (`id`) ON UPDATE NO ACTION ON DELETE RESTRICT,
  CONSTRAINT `1` FOREIGN KEY (`summary_id`) REFERENCES `ctx_summaries` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create "ctx_items" table
CREATE TABLE `ctx_items` (
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
-- Create index "idx_ctx_items_conv" to table: "ctx_items"
CREATE INDEX `idx_ctx_items_conv` ON `ctx_items` (`conversation_id`, `ordinal`);
-- Create "sched_jobs" table
CREATE TABLE `sched_jobs` (
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
  `org_id` text NOT NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  `last_run_at` text NULL,
  `last_error` text NOT NULL DEFAULT '',
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`org_id`) REFERENCES `auth_organization` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "idx_sched_jobs_owner" to table: "sched_jobs"
CREATE INDEX `idx_sched_jobs_owner` ON `sched_jobs` (`owner_kind`, `plugin_id`, `job_key`);
-- Create index "idx_sched_jobs_org_id" to table: "sched_jobs"
CREATE INDEX `idx_sched_jobs_org_id` ON `sched_jobs` (`org_id`);
-- Create "sched_job_runs" table
CREATE TABLE `sched_job_runs` (
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
-- Create index "idx_sched_job_runs_job_id" to table: "sched_job_runs"
CREATE INDEX `idx_sched_job_runs_job_id` ON `sched_job_runs` (`job_id`, `started_at` DESC);
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
  `org_id` text NOT NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`org_id`) REFERENCES `auth_organization` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CHECK (effect IN ('allow', 'deny'))
);
-- Create index "idx_auth_policies_org_id" to table: "auth_policies"
CREATE INDEX `idx_auth_policies_org_id` ON `auth_policies` (`org_id`);
-- Create "auth_user_agents" table
CREATE TABLE `auth_user_agents` (
  `user_id` text NOT NULL,
  `agent_id` text NOT NULL,
  PRIMARY KEY (`user_id`, `agent_id`),
  CONSTRAINT `0` FOREIGN KEY (`agent_id`) REFERENCES `settings_agents` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `1` FOREIGN KEY (`user_id`) REFERENCES `auth_user` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create "auth_user_tokens" table
CREATE TABLE `auth_user_tokens` (
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
-- Create index "auth_user_tokens_token_hash" to table: "auth_user_tokens"
CREATE UNIQUE INDEX `auth_user_tokens_token_hash` ON `auth_user_tokens` (`token_hash`);
-- Create index "idx_auth_user_tokens_auto_active" to table: "auth_user_tokens"
CREATE UNIQUE INDEX `idx_auth_user_tokens_auto_active` ON `auth_user_tokens` (`user_id`) WHERE auto_generated = 1 AND revoked_at IS NULL;
-- Create "auth_user" table
CREATE TABLE `auth_user` (
  `id` text NOT NULL,
  `email` text NOT NULL,
  `name` text NOT NULL DEFAULT '',
  `avatar_url` text NOT NULL DEFAULT '',
  `default_agent_id` text NULL,
  `notify_identity_id` text NULL,
  `age_public_key` text NOT NULL DEFAULT '',
  `age_private_key` text NOT NULL DEFAULT '',
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`notify_identity_id`) REFERENCES `channel_identity` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `1` FOREIGN KEY (`default_agent_id`) REFERENCES `settings_agents` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
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
-- Create "auth_membership" table
CREATE TABLE `auth_membership` (
  `id` text NOT NULL,
  `user_id` text NOT NULL,
  `organization_id` text NOT NULL,
  `role` text NOT NULL DEFAULT 'user',
  `is_active` integer NOT NULL DEFAULT 1,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`organization_id`) REFERENCES `auth_organization` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `1` FOREIGN KEY (`user_id`) REFERENCES `auth_user` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "auth_membership_user_id_organization_id" to table: "auth_membership"
CREATE UNIQUE INDEX `auth_membership_user_id_organization_id` ON `auth_membership` (`user_id`, `organization_id`);
-- Create index "idx_auth_membership_user_id" to table: "auth_membership"
CREATE INDEX `idx_auth_membership_user_id` ON `auth_membership` (`user_id`);
-- Create index "idx_auth_membership_organization_id" to table: "auth_membership"
CREATE INDEX `idx_auth_membership_organization_id` ON `auth_membership` (`organization_id`);
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
-- Create "auth_oidc_codes" table
CREATE TABLE `auth_oidc_codes` (
  `id` text NOT NULL,
  `code_hash` text NOT NULL,
  `user_id` text NOT NULL,
  `org_id` text NOT NULL DEFAULT '',
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
-- Create index "auth_oidc_codes_code_hash" to table: "auth_oidc_codes"
CREATE UNIQUE INDEX `auth_oidc_codes_code_hash` ON `auth_oidc_codes` (`code_hash`);
-- Create index "idx_auth_oidc_codes_code_hash" to table: "auth_oidc_codes"
CREATE INDEX `idx_auth_oidc_codes_code_hash` ON `auth_oidc_codes` (`code_hash`);
-- Create index "idx_auth_oidc_codes_user_id" to table: "auth_oidc_codes"
CREATE INDEX `idx_auth_oidc_codes_user_id` ON `auth_oidc_codes` (`user_id`);
-- Create "auth_oidc_access_tokens" table
CREATE TABLE `auth_oidc_access_tokens` (
  `id` text NOT NULL,
  `token_hash` text NOT NULL,
  `user_id` text NOT NULL,
  `org_id` text NOT NULL DEFAULT '',
  `client_id` text NOT NULL,
  `scopes` text NOT NULL DEFAULT '[]',
  `expires_at` text NOT NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`user_id`) REFERENCES `auth_user` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "auth_oidc_access_tokens_token_hash" to table: "auth_oidc_access_tokens"
CREATE UNIQUE INDEX `auth_oidc_access_tokens_token_hash` ON `auth_oidc_access_tokens` (`token_hash`);
-- Create index "idx_auth_oidc_access_tokens_token_hash" to table: "auth_oidc_access_tokens"
CREATE INDEX `idx_auth_oidc_access_tokens_token_hash` ON `auth_oidc_access_tokens` (`token_hash`);
-- Create index "idx_auth_oidc_access_tokens_user_id" to table: "auth_oidc_access_tokens"
CREATE INDEX `idx_auth_oidc_access_tokens_user_id` ON `auth_oidc_access_tokens` (`user_id`);
-- Create "auth_invite" table
CREATE TABLE `auth_invite` (
  `id` text NOT NULL,
  `token_hash` text NOT NULL,
  `org_id` text NOT NULL,
  `email` text NULL,
  `role` text NOT NULL DEFAULT 'user',
  `status` text NOT NULL DEFAULT 'pending',
  `max_uses` integer NOT NULL DEFAULT 1,
  `use_count` integer NOT NULL DEFAULT 0,
  `invited_by` text NOT NULL,
  `accepted_by` text NULL,
  `expires_at` text NOT NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`accepted_by`) REFERENCES `auth_user` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION,
  CONSTRAINT `1` FOREIGN KEY (`invited_by`) REFERENCES `auth_user` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION,
  CONSTRAINT `2` FOREIGN KEY (`org_id`) REFERENCES `auth_organization` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CHECK (status IN ('pending','accepted','revoked'))
);
-- Create index "auth_invite_token_hash" to table: "auth_invite"
CREATE UNIQUE INDEX `auth_invite_token_hash` ON `auth_invite` (`token_hash`);
-- Create index "idx_auth_invite_org" to table: "auth_invite"
CREATE INDEX `idx_auth_invite_org` ON `auth_invite` (`org_id`);
-- Create index "idx_auth_invite_token_hash" to table: "auth_invite"
CREATE INDEX `idx_auth_invite_token_hash` ON `auth_invite` (`token_hash`);
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
-- Create "plugin_state_entries" table
CREATE TABLE `plugin_state_entries` (
  `plugin_id` text NOT NULL,
  `scope_kind` text NOT NULL,
  `scope_id` text NOT NULL DEFAULT '',
  `state_key` text NOT NULL,
  `value` text NOT NULL DEFAULT '{}',
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`plugin_id`, `scope_kind`, `scope_id`, `state_key`)
);
-- Create "skills" table
CREATE TABLE `skills` (
  `id` text NULL,
  `scope` text NOT NULL,
  `user_id` text NULL,
  `agent_id` text NULL,
  `name` text NOT NULL,
  `description` text NOT NULL,
  `status` text NOT NULL DEFAULT 'active',
  `disable_model_invocation` integer NOT NULL DEFAULT 0,
  `metadata` text NOT NULL DEFAULT '{}',
  `org_id` text NOT NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`org_id`) REFERENCES `auth_organization` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `1` FOREIGN KEY (`agent_id`) REFERENCES `settings_agents` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `2` FOREIGN KEY (`user_id`) REFERENCES `auth_user` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CHECK (scope IN ('system','agent','user')),
  CHECK (status IN ('draft','active','deprecated')),
  CHECK (
        (scope='system'  AND user_id IS NULL     AND agent_id IS NULL) OR
        (scope='agent'   AND user_id IS NULL     AND agent_id IS NOT NULL) OR
        (scope='user'    AND user_id IS NOT NULL)
    )
);
-- Create index "idx_skills_owner_name" to table: "skills"
CREATE UNIQUE INDEX `idx_skills_owner_name` ON `skills` (`name`, `scope`, (ifnull(user_id, 0)), (ifnull(agent_id, '')));
-- Create index "idx_skills_visibility" to table: "skills"
CREATE INDEX `idx_skills_visibility` ON `skills` (`scope`, `user_id`, `agent_id`);
-- Create index "idx_skills_org_id" to table: "skills"
CREATE INDEX `idx_skills_org_id` ON `skills` (`org_id`);
-- Create "skill_files" table
CREATE TABLE `skill_files` (
  `skill_id` text NOT NULL,
  `path` text NOT NULL,
  `content` text NOT NULL,
  PRIMARY KEY (`skill_id`, `path`),
  CONSTRAINT `0` FOREIGN KEY (`skill_id`) REFERENCES `skills` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create "vault_entries" table
CREATE TABLE `vault_entries` (
  `id` text NULL,
  `user_id` text NOT NULL,
  `name` text NOT NULL,
  `ciphertext` text NOT NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`user_id`) REFERENCES `auth_user` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "vault_entries_user_id_name" to table: "vault_entries"
CREATE UNIQUE INDEX `vault_entries_user_id_name` ON `vault_entries` (`user_id`, `name`);
-- Create "auth_oauth_provider" table
CREATE TABLE `auth_oauth_provider` (
  `id` text NULL,
  `provider_id` text NOT NULL,
  `client_id` text NOT NULL DEFAULT '',
  `client_secret_enc` text NOT NULL DEFAULT '',
  `redirect_url` text NOT NULL DEFAULT '',
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`)
);
-- Create index "auth_oauth_provider_provider_id" to table: "auth_oauth_provider"
CREATE UNIQUE INDEX `auth_oauth_provider_provider_id` ON `auth_oauth_provider` (`provider_id`);
-- Create "articles" table
CREATE TABLE `articles` (
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
  CONSTRAINT `1` FOREIGN KEY (`user_id`) REFERENCES `auth_user` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CHECK (source_type IN ('web','twitter','youtube','github','rss','pdf')),
  CHECK (status IN ('unread','read','archived'))
);
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
-- Create "rss_feeds" table
CREATE TABLE `rss_feeds` (
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
  CONSTRAINT `1` FOREIGN KEY (`user_id`) REFERENCES `auth_user` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "idx_rss_feeds_user_url" to table: "rss_feeds"
CREATE UNIQUE INDEX `idx_rss_feeds_user_url` ON `rss_feeds` (`user_id`, `url`);
-- Create "rss_feed_entries" table
CREATE TABLE `rss_feed_entries` (
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
  CONSTRAINT `0` FOREIGN KEY (`article_id`) REFERENCES `articles` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `1` FOREIGN KEY (`feed_id`) REFERENCES `rss_feeds` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CHECK (status IN ('pending','saved','skipped','error'))
);
-- Create index "idx_rss_entries_feed_guid" to table: "rss_feed_entries"
CREATE UNIQUE INDEX `idx_rss_entries_feed_guid` ON `rss_feed_entries` (`feed_id`, `guid`);
-- Create index "idx_rss_entries_status" to table: "rss_feed_entries"
CREATE INDEX `idx_rss_entries_status` ON `rss_feed_entries` (`status`);
-- Create "recally_digests" table
CREATE TABLE `recally_digests` (
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
-- Create index "idx_recally_digests_user_date" to table: "recally_digests"
CREATE UNIQUE INDEX `idx_recally_digests_user_date` ON `recally_digests` (`user_id`, `date`);
-- Create index "idx_recally_digests_user_id" to table: "recally_digests"
CREATE INDEX `idx_recally_digests_user_id` ON `recally_digests` (`user_id`);
-- Create "recally_digest_articles" table
CREATE TABLE `recally_digest_articles` (
  `digest_id` text NOT NULL,
  `article_id` text NOT NULL,
  `section` text NOT NULL,
  `position` integer NOT NULL DEFAULT 0,
  PRIMARY KEY (`digest_id`, `article_id`, `section`),
  CONSTRAINT `0` FOREIGN KEY (`article_id`) REFERENCES `articles` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `1` FOREIGN KEY (`digest_id`) REFERENCES `recally_digests` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CHECK (section IN ('saved_yesterday', 'worth_revisiting'))
);
-- Create index "idx_recally_digest_articles_digest" to table: "recally_digest_articles"
CREATE INDEX `idx_recally_digest_articles_digest` ON `recally_digest_articles` (`digest_id`);
-- Create "agent_task" table
CREATE TABLE `agent_task` (
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
  CONSTRAINT `0` FOREIGN KEY (`user_id`) REFERENCES `auth_user` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `1` FOREIGN KEY (`agent_id`) REFERENCES `settings_agents` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `2` FOREIGN KEY (`scheduler_run_id`) REFERENCES `sched_job_runs` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `3` FOREIGN KEY (`scheduler_job_id`) REFERENCES `sched_jobs` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CHECK (status IN ('pending','running','blocked','review_requested','done','failed','cancelled')),
  CHECK (priority IN ('routine','urgent'))
);
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
-- Create "agent_task_event" table
CREATE TABLE `agent_task_event` (
  `id` text NOT NULL,
  `task_id` text NOT NULL,
  `event_type` text NOT NULL,
  `detail` text NOT NULL DEFAULT '{}',
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`task_id`) REFERENCES `agent_task` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "idx_agent_task_event_task_id" to table: "agent_task_event"
CREATE INDEX `idx_agent_task_event_task_id` ON `agent_task_event` (`task_id`, `created_at` DESC);
-- Create "projects" table
CREATE TABLE `projects` (
  `id` text NULL,
  `agent_id` text NOT NULL,
  `user_id` text NOT NULL,
  `name` text NOT NULL,
  `base_dir` text NOT NULL,
  `description` text NULL,
  `archived` integer NOT NULL DEFAULT 0,
  `org_id` text NOT NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`org_id`) REFERENCES `auth_organization` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "projects_agent_id_user_id_name" to table: "projects"
CREATE UNIQUE INDEX `projects_agent_id_user_id_name` ON `projects` (`agent_id`, `user_id`, `name`);
-- Create index "idx_projects_org_id" to table: "projects"
CREATE INDEX `idx_projects_org_id` ON `projects` (`org_id`);
