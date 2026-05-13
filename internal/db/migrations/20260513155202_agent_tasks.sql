-- Create "agent_tasks" table
CREATE TABLE `agent_tasks` (
  `id` text NULL,
  `title` text NOT NULL,
  `description` text NOT NULL DEFAULT '',
  `status` text NOT NULL DEFAULT 'pending',
  `priority` text NOT NULL DEFAULT 'routine',
  `session_id` text NOT NULL DEFAULT '',
  `context` text NOT NULL DEFAULT '{}',
  `review_request` text NOT NULL DEFAULT '',
  `agent_id` text NOT NULL DEFAULT '',
  `user_id` integer NOT NULL DEFAULT 0,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CHECK (status IN ('pending','running','blocked','review_requested','done','failed','cancelled')),
  CHECK (priority IN ('routine','urgent'))
);
-- Create index "idx_agent_tasks_status" to table: "agent_tasks"
CREATE INDEX `idx_agent_tasks_status` ON `agent_tasks` (`status`);
-- Create index "idx_agent_tasks_user_id" to table: "agent_tasks"
CREATE INDEX `idx_agent_tasks_user_id` ON `agent_tasks` (`user_id`);
-- Create "agent_task_events" table
CREATE TABLE `agent_task_events` (
  `id` text NULL,
  `task_id` text NOT NULL,
  `event_type` text NOT NULL,
  `detail` text NOT NULL DEFAULT '{}',
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`task_id`) REFERENCES `agent_tasks` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CHECK (event_type IN ('started','progress','blocked','review_requested','done','failed','cancelled'))
);
-- Create index "idx_agent_task_events_task_id" to table: "agent_task_events"
CREATE INDEX `idx_agent_task_events_task_id` ON `agent_task_events` (`task_id`, `created_at` DESC);
