-- Create "artifact_share" table
CREATE TABLE `artifact_share` (
  `id` text NULL,
  `token_hash` text NOT NULL,
  `user_id` text NOT NULL,
  `session_id` text NOT NULL,
  `path` text NOT NULL,
  `media_type` text NOT NULL,
  `content` blob NOT NULL,
  `expires_at` text NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`user_id`) REFERENCES `auth_users` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "artifact_share_token_hash" to table: "artifact_share"
CREATE UNIQUE INDEX `artifact_share_token_hash` ON `artifact_share` (`token_hash`);
-- Create index "idx_artifact_share_user" to table: "artifact_share"
CREATE INDEX `idx_artifact_share_user` ON `artifact_share` (`user_id`, `created_at` DESC);
