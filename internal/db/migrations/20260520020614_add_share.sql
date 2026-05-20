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
  CONSTRAINT `0` FOREIGN KEY (`user_id`) REFERENCES `auth_users` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "share_token_hash" to table: "share"
CREATE UNIQUE INDEX `share_token_hash` ON `share` (`token_hash`);
-- Create index "idx_share_user" to table: "share"
CREATE INDEX `idx_share_user` ON `share` (`user_id`, `created_at` DESC);
