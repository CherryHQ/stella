-- Create "settings_plugins" table
CREATE TABLE `settings_plugins` (
  `id` text NULL,
  `kind` text NOT NULL,
  `name` text NOT NULL,
  `enabled` integer NOT NULL DEFAULT 1,
  `config` text NOT NULL DEFAULT '{}',
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`)
);
