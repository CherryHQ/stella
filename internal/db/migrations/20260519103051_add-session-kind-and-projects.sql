-- Add column "kind" to table: "ctx_conversations"
ALTER TABLE `ctx_conversations` ADD COLUMN `kind` text NOT NULL DEFAULT 'chat';
-- Add column "project_id" to table: "ctx_conversations"
ALTER TABLE `ctx_conversations` ADD COLUMN `project_id` text NULL;
-- Backfill kind for task sessions
UPDATE `ctx_conversations` SET kind = 'task', channel = 'system'
  WHERE channel = 'task' OR session_id LIKE 'task:%';
-- Backfill kind for scheduler sessions
UPDATE `ctx_conversations` SET kind = 'scheduler', channel = 'system'
  WHERE session_id LIKE '%:scheduler:%' OR session_id LIKE 'scheduler:%';
-- Create index "idx_one_agent_main" to table: "ctx_conversations"
CREATE UNIQUE INDEX `idx_one_agent_main` ON `ctx_conversations` (`agent_id`, `user_id`) WHERE kind = 'main' AND project_id IS NULL AND archived = 0;
-- Create index "idx_one_project_main" to table: "ctx_conversations"
CREATE UNIQUE INDEX `idx_one_project_main` ON `ctx_conversations` (`project_id`) WHERE kind = 'main' AND project_id IS NOT NULL AND archived = 0;
-- Create "projects" table
CREATE TABLE `projects` (
  `id` text NULL,
  `agent_id` text NOT NULL,
  `user_id` text NOT NULL,
  `name` text NOT NULL,
  `base_dir` text NOT NULL,
  `description` text NULL,
  `archived` integer NOT NULL DEFAULT 0,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`)
);
-- Create index "projects_agent_id_user_id_name" to table: "projects"
CREATE UNIQUE INDEX `projects_agent_id_user_id_name` ON `projects` (`agent_id`, `user_id`, `name`);
