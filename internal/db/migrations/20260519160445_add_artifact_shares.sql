-- Create "artifact_shares" table
CREATE TABLE `artifact_shares` (
  `id` text NULL,
  `token_hash` text NOT NULL,
  `owner_user_id` text NOT NULL,
  `source_session_id` text NOT NULL,
  `source_path` text NOT NULL,
  `title` text NOT NULL,
  `media_type` text NOT NULL,
  `kind` text NOT NULL,
  `content` blob NOT NULL,
  `size_bytes` integer NOT NULL,
  `expires_at` text NULL,
  `revoked_at` text NULL,
  `last_accessed_at` text NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`owner_user_id`) REFERENCES `auth_users` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CHECK (kind IN ('html', 'markdown', 'image', 'pdf')),
  CHECK (size_bytes >= 0)
);
-- Create index "artifact_shares_token_hash" to table: "artifact_shares"
CREATE UNIQUE INDEX `artifact_shares_token_hash` ON `artifact_shares` (`token_hash`);
-- Create index "idx_artifact_shares_owner_user_id_created_at" to table: "artifact_shares"
CREATE INDEX `idx_artifact_shares_owner_user_id_created_at` ON `artifact_shares` (`owner_user_id`, `created_at` DESC);
-- Create index "idx_artifact_shares_source_session_id" to table: "artifact_shares"
CREATE INDEX `idx_artifact_shares_source_session_id` ON `artifact_shares` (`source_session_id`);
-- Create index "idx_artifact_shares_expires_at" to table: "artifact_shares"
CREATE INDEX `idx_artifact_shares_expires_at` ON `artifact_shares` (`expires_at`);
