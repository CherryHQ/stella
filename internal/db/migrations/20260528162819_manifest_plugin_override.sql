-- Create "settings_manifest_plugin_override" table
CREATE TABLE `settings_manifest_plugin_override` (
  `plugin_id` text NOT NULL,
  `org_id` text NOT NULL,
  `enabled` integer NULL,
  `session_env_vault_key` text NOT NULL DEFAULT '',
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`plugin_id`, `org_id`),
  CONSTRAINT `0` FOREIGN KEY (`org_id`) REFERENCES `auth_organization` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "idx_manifest_plugin_override_org" to table: "settings_manifest_plugin_override"
CREATE INDEX `idx_manifest_plugin_override_org` ON `settings_manifest_plugin_override` (`org_id`);
