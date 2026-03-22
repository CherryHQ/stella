-- Create "feishu_tokens" table
CREATE TABLE `feishu_tokens` (
  `open_id` text NULL,
  `access_token` text NOT NULL,
  `refresh_token` text NOT NULL,
  `expires_at` text NOT NULL,
  `refresh_expires_at` text NOT NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`open_id`)
);
