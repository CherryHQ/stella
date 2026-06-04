-- Add column "profile_entries" to table: "ctx_agent_memory"
ALTER TABLE `ctx_agent_memory` ADD COLUMN `profile_entries` text NOT NULL DEFAULT '[]';
-- Create "ctx_group_memory" table
CREATE TABLE `ctx_group_memory` (
  `group_id` text NOT NULL,
  `content` text NOT NULL DEFAULT '',
  `version` integer NOT NULL DEFAULT 0,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`group_id`),
  CONSTRAINT `0` FOREIGN KEY (`group_id`) REFERENCES `ctx_group_state` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
