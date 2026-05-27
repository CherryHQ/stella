-- Disable the enforcement of foreign-keys constraints
PRAGMA foreign_keys = off;
-- Create "new_settings" table
CREATE TABLE `new_settings` (
  `key` text NOT NULL,
  `value` text NOT NULL DEFAULT '{}',
  `org_id` text NOT NULL,
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`key`, `org_id`),
  CONSTRAINT `0` FOREIGN KEY (`org_id`) REFERENCES `auth_organization` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Copy rows from old table "settings" to new temporary table "new_settings"
INSERT INTO `new_settings` (`key`, `value`, `org_id`, `updated_at`) SELECT `key`, `value`, `org_id`, `updated_at` FROM `settings`;
-- Drop "settings" table after copying rows
DROP TABLE `settings`;
-- Rename temporary table "new_settings" to "settings"
ALTER TABLE `new_settings` RENAME TO `settings`;
-- Create index "idx_settings_org_id" to table: "settings"
CREATE INDEX `idx_settings_org_id` ON `settings` (`org_id`);
-- Enable back the enforcement of foreign-keys constraints
PRAGMA foreign_keys = on;
