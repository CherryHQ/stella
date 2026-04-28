-- Create "memory_snapshots" table
CREATE TABLE `memory_snapshots` (
  `session_id` text NOT NULL,
  `user_id` integer NOT NULL,
  `agent_id` text NOT NULL,
  `version` integer NOT NULL DEFAULT 0,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`session_id`, `user_id`, `agent_id`)
);
-- Create index "idx_memory_snapshots_user_agent" to table: "memory_snapshots"
CREATE INDEX `idx_memory_snapshots_user_agent` ON `memory_snapshots` (`user_id`, `agent_id`);
