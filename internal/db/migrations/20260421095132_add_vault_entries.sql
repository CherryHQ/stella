-- Add column "age_public_key" to table: "auth_users"
ALTER TABLE `auth_users` ADD COLUMN `age_public_key` text NOT NULL DEFAULT '';
-- Add column "age_private_key" to table: "auth_users"
ALTER TABLE `auth_users` ADD COLUMN `age_private_key` text NOT NULL DEFAULT '';
-- Create "vault_entries" table
CREATE TABLE `vault_entries` (
  `id` integer NULL PRIMARY KEY AUTOINCREMENT,
  `user_id` integer NOT NULL,
  `name` text NOT NULL,
  `ciphertext` text NOT NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  CONSTRAINT `0` FOREIGN KEY (`user_id`) REFERENCES `auth_users` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "vault_entries_user_id_name" to table: "vault_entries"
CREATE UNIQUE INDEX `vault_entries_user_id_name` ON `vault_entries` (`user_id`, `name`);
