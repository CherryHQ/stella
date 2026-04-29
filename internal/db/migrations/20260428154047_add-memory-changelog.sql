-- Add column "version" to table: "ctx_agent_memory"
ALTER TABLE `ctx_agent_memory` ADD COLUMN `version` integer NOT NULL DEFAULT 0;
-- Create "memory_changelog" table
CREATE TABLE `memory_changelog` (
  `id` integer NULL PRIMARY KEY AUTOINCREMENT,
  `user_id` integer NOT NULL,
  `agent_id` text NOT NULL,
  `session_id` text NULL,
  `entity_id` text NULL,
  `scope` text NOT NULL,
  `action` text NOT NULL,
  `source` text NOT NULL,
  `memory_version_before` integer NULL,
  `memory_version_after` integer NULL,
  `before_text` text NULL,
  `after_text` text NULL,
  `metadata` text NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  CHECK (scope IN ('profile', 'soul', 'constraint', 'skill', 'compaction')),
  CHECK (action IN ('create', 'update', 'delete', 'compact')),
  CHECK (source IN ('user', 'agent', 'reflect', 'system'))
);
-- Create index "idx_memory_changelog_user_agent" to table: "memory_changelog"
CREATE INDEX `idx_memory_changelog_user_agent` ON `memory_changelog` (`user_id`, `agent_id`, `scope`);
-- Create index "idx_memory_changelog_version" to table: "memory_changelog"
CREATE INDEX `idx_memory_changelog_version` ON `memory_changelog` (`user_id`, `agent_id`, `scope`, `memory_version_after`);
-- Create index "idx_memory_changelog_session" to table: "memory_changelog"
CREATE INDEX `idx_memory_changelog_session` ON `memory_changelog` (`session_id`);
-- Create index "idx_memory_changelog_created" to table: "memory_changelog"
CREATE INDEX `idx_memory_changelog_created` ON `memory_changelog` (`created_at`);
