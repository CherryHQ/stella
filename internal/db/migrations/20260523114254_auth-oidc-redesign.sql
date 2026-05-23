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
