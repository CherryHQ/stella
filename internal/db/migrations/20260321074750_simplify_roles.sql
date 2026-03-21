-- Disable the enforcement of foreign-keys constraints
PRAGMA foreign_keys = off;
-- Drop "auth_roles" table
DROP TABLE `auth_roles`;
-- Drop "auth_user_roles" table
DROP TABLE `auth_user_roles`;
-- Add column "role" to table: "auth_users"
ALTER TABLE `auth_users` ADD COLUMN `role` text NOT NULL DEFAULT 'user';
-- Enable back the enforcement of foreign-keys constraints
PRAGMA foreign_keys = on;
