-- Create "plugin_state_entries" table
CREATE TABLE `plugin_state_entries` (
  `plugin_id` text NOT NULL,
  `scope_kind` text NOT NULL,
  `scope_id` text NOT NULL DEFAULT '',
  `state_key` text NOT NULL,
  `value` text NOT NULL DEFAULT '{}',
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`plugin_id`, `scope_kind`, `scope_id`, `state_key`)
);

-- Migrate reflect review watermarks into plugin-owned state entries.
INSERT INTO `plugin_state_entries` (`plugin_id`, `scope_kind`, `scope_id`, `state_key`, `value`, `created_at`, `updated_at`)
SELECT 'reflect', 'session', `session_id`, 'review_watermark', json_object('reviewed_at', `reviewed_at`), datetime('now'), datetime('now')
FROM `reflect_watermarks`;

-- Drop "reflect_watermarks" table
DROP TABLE `reflect_watermarks`;
