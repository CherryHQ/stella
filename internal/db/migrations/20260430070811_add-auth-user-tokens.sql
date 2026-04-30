-- Create "auth_user_tokens" table
CREATE TABLE `auth_user_tokens` (
  `id` integer NULL PRIMARY KEY AUTOINCREMENT,
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
  CONSTRAINT `0` FOREIGN KEY (`user_id`) REFERENCES `auth_users` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "auth_user_tokens_token_hash" to table: "auth_user_tokens"
CREATE UNIQUE INDEX `auth_user_tokens_token_hash` ON `auth_user_tokens` (`token_hash`);
-- Create index "idx_auth_user_tokens_auto_active" to table: "auth_user_tokens"
CREATE UNIQUE INDEX `idx_auth_user_tokens_auto_active` ON `auth_user_tokens` (`user_id`) WHERE auto_generated = 1 AND revoked_at IS NULL;
