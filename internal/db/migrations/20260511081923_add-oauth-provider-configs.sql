-- Create "oauth_provider_configs" table
CREATE TABLE `oauth_provider_configs` (
  `provider_id` text NULL,
  `client_id` text NOT NULL DEFAULT '',
  `client_secret` text NOT NULL DEFAULT '',
  `redirect_url` text NOT NULL DEFAULT '',
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`provider_id`)
);
