-- Disable the enforcement of foreign-keys constraints
PRAGMA foreign_keys = off;
-- Create "new_agent_task" table
CREATE TABLE `new_agent_task` (
  `id` text NOT NULL,
  `title` text NOT NULL,
  `description` text NOT NULL DEFAULT '',
  `status` text NOT NULL DEFAULT 'pending',
  `priority` text NOT NULL DEFAULT 'routine',
  `session_id` text NULL,
  `context` text NOT NULL DEFAULT '{}',
  `review_request` text NOT NULL DEFAULT '{}',
  `notify_at` text NULL,
  `scheduler_job_id` text NULL,
  `scheduler_run_id` text NULL,
  `agent_id` text NULL,
  `user_id` text NOT NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`user_id`) REFERENCES `auth_user` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `1` FOREIGN KEY (`agent_id`) REFERENCES `settings_agent` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `2` FOREIGN KEY (`scheduler_run_id`) REFERENCES `sched_job_run` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `3` FOREIGN KEY (`scheduler_job_id`) REFERENCES `sched_job` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CHECK (status IN ('pending','running','blocked','review_requested','done','failed','cancelled')),
  CHECK (priority IN ('routine','urgent'))
);
-- Copy rows from old table "agent_task" to new temporary table "new_agent_task"
INSERT INTO `new_agent_task` (`id`, `title`, `description`, `status`, `priority`, `session_id`, `context`, `review_request`, `notify_at`, `scheduler_job_id`, `scheduler_run_id`, `agent_id`, `user_id`, `created_at`, `updated_at`) SELECT `id`, `title`, `description`, `status`, `priority`, `session_id`, `context`, `review_request`, `notify_at`, `scheduler_job_id`, `scheduler_run_id`, `agent_id`, `user_id`, `created_at`, `updated_at` FROM `agent_task`;
-- Drop "agent_task" table after copying rows
DROP TABLE `agent_task`;
-- Rename temporary table "new_agent_task" to "agent_task"
ALTER TABLE `new_agent_task` RENAME TO `agent_task`;
-- Create index "idx_agent_task_user_id_status" to table: "agent_task"
CREATE INDEX `idx_agent_task_user_id_status` ON `agent_task` (`user_id`, `status`);
-- Create index "idx_agent_task_status" to table: "agent_task"
CREATE INDEX `idx_agent_task_status` ON `agent_task` (`status`);
-- Create index "idx_agent_task_session_id" to table: "agent_task"
CREATE INDEX `idx_agent_task_session_id` ON `agent_task` (`session_id`);
-- Create index "idx_agent_task_scheduler_job_id" to table: "agent_task"
CREATE INDEX `idx_agent_task_scheduler_job_id` ON `agent_task` (`scheduler_job_id`);
-- Create index "idx_agent_task_scheduler_run_id" to table: "agent_task"
CREATE INDEX `idx_agent_task_scheduler_run_id` ON `agent_task` (`scheduler_run_id`);
-- Create index "idx_agent_task_agent_id" to table: "agent_task"
CREATE INDEX `idx_agent_task_agent_id` ON `agent_task` (`agent_id`);
-- Create "agent_task_dep" table
CREATE TABLE `agent_task_dep` (
  `task_id` text NOT NULL,
  `dep_id` text NOT NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`task_id`, `dep_id`),
  CONSTRAINT `0` FOREIGN KEY (`dep_id`) REFERENCES `agent_task` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `1` FOREIGN KEY (`task_id`) REFERENCES `agent_task` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CHECK (task_id != dep_id)
);
-- Create index "idx_agent_task_dep_dep_id" to table: "agent_task_dep"
CREATE INDEX `idx_agent_task_dep_dep_id` ON `agent_task_dep` (`dep_id`);
-- Enable back the enforcement of foreign-keys constraints
PRAGMA foreign_keys = on;
