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
