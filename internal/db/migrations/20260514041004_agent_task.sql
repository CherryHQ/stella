-- Create "agent_task" table
CREATE TABLE `agent_task` (
  `id` text NOT NULL,
  `title` text NOT NULL,
  `description` text NOT NULL DEFAULT '',
  `status` text NOT NULL DEFAULT 'pending',
  `priority` text NOT NULL DEFAULT 'routine',
  `session_id` text NULL,
  `context` text NOT NULL DEFAULT '{}',
  `review_request` text NOT NULL DEFAULT '',
  `agent_id` text NULL,
  `user_id` integer NOT NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`user_id`) REFERENCES `auth_users` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `1` FOREIGN KEY (`agent_id`) REFERENCES `settings_agents` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `2` FOREIGN KEY (`session_id`) REFERENCES `auth_sessions` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL
);
-- Create index "idx_agent_task_user_id_status" to table: "agent_task"
CREATE INDEX `idx_agent_task_user_id_status` ON `agent_task` (`user_id`, `status`);
-- Create index "idx_agent_task_status" to table: "agent_task"
CREATE INDEX `idx_agent_task_status` ON `agent_task` (`status`);
-- Create index "idx_agent_task_session_id" to table: "agent_task"
CREATE INDEX `idx_agent_task_session_id` ON `agent_task` (`session_id`);
-- Create index "idx_agent_task_agent_id" to table: "agent_task"
CREATE INDEX `idx_agent_task_agent_id` ON `agent_task` (`agent_id`);
-- Create "agent_task_event" table
CREATE TABLE `agent_task_event` (
  `id` text NOT NULL,
  `task_id` text NOT NULL,
  `event_type` text NOT NULL,
  `detail` text NOT NULL DEFAULT '{}',
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`task_id`) REFERENCES `agent_task` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "idx_agent_task_event_task_id" to table: "agent_task_event"
CREATE INDEX `idx_agent_task_event_task_id` ON `agent_task_event` (`task_id`, `created_at` DESC);
