-- Create "auth_oauth_provider" table
CREATE TABLE `auth_oauth_provider` (
  `id` integer NULL PRIMARY KEY AUTOINCREMENT,
  `provider_id` text NOT NULL,
  `client_id` text NOT NULL DEFAULT '',
  `client_secret_enc` text NOT NULL DEFAULT '',
  `redirect_url` text NOT NULL DEFAULT '',
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now'))
);
-- Create index "auth_oauth_provider_provider_id" to table: "auth_oauth_provider"
CREATE UNIQUE INDEX `auth_oauth_provider_provider_id` ON `auth_oauth_provider` (`provider_id`);
