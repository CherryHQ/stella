-- Disable the enforcement of foreign-keys constraints
PRAGMA foreign_keys = off;
-- Create "new_vault_entry" table
CREATE TABLE `new_vault_entry` (
  `id` text NULL,
  `scope` text NOT NULL DEFAULT 'user',
  `user_id` text NULL,
  `agent_id` text NULL,
  `name` text NOT NULL,
  `ciphertext` text NOT NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`agent_id`) REFERENCES `agent` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `1` FOREIGN KEY (`user_id`) REFERENCES `auth_user` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CHECK (
        (scope = 'user'         AND user_id IS NOT NULL AND agent_id IS NULL) OR
        (scope = 'user_agent'   AND user_id IS NOT NULL AND agent_id IS NOT NULL) OR
        (scope = 'system'       AND user_id IS NULL     AND agent_id IS NULL) OR
        (scope = 'system_agent' AND user_id IS NULL     AND agent_id IS NOT NULL)
    )
);
-- Copy rows from old table "vault_entry" to new temporary table "new_vault_entry"
INSERT INTO `new_vault_entry` (`id`, `user_id`, `name`, `ciphertext`, `created_at`, `updated_at`) SELECT `id`, `user_id`, `name`, `ciphertext`, `created_at`, `updated_at` FROM `vault_entry`;
-- Drop "vault_entry" table after copying rows
DROP TABLE `vault_entry`;
-- Rename temporary table "new_vault_entry" to "vault_entry"
ALTER TABLE `new_vault_entry` RENAME TO `vault_entry`;
-- Create index "uniq_vault_entry_scope_key" to table: "vault_entry"
CREATE UNIQUE INDEX `uniq_vault_entry_scope_key` ON `vault_entry` (`scope`, (ifnull(user_id, '')), (ifnull(agent_id, '')), `name`);
-- Create index "idx_vault_entry_scope" to table: "vault_entry"
CREATE INDEX `idx_vault_entry_scope` ON `vault_entry` (`scope`, `user_id`, `agent_id`);
-- Enable back the enforcement of foreign-keys constraints
PRAGMA foreign_keys = on;
