-- Disable the enforcement of foreign-keys constraints
PRAGMA foreign_keys = off;
-- Create "new_ctx_agent_memory" table
CREATE TABLE `new_ctx_agent_memory` (
  `user_id` integer NOT NULL,
  `agent_id` text NOT NULL,
  `content` text NOT NULL DEFAULT '',
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`user_id`, `agent_id`),
  CONSTRAINT `0` FOREIGN KEY (`agent_id`) REFERENCES `settings_agents` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Copy rows from old table "ctx_agent_memory" to new temporary table "new_ctx_agent_memory"
INSERT INTO `new_ctx_agent_memory` (`user_id`, `agent_id`, `content`, `updated_at`) SELECT `user_id`, `agent_id`, `content`, `updated_at` FROM `ctx_agent_memory`;
-- Drop "ctx_agent_memory" table after copying rows
DROP TABLE `ctx_agent_memory`;
-- Rename temporary table "new_ctx_agent_memory" to "ctx_agent_memory"
ALTER TABLE `new_ctx_agent_memory` RENAME TO `ctx_agent_memory`;
-- Enable back the enforcement of foreign-keys constraints
PRAGMA foreign_keys = on;
