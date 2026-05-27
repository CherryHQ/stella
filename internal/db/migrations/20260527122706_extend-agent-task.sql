-- Disable the enforcement of foreign-keys constraints
PRAGMA foreign_keys = off;
-- Create "new_agent_task" table
CREATE TABLE `new_agent_task` (
  `id` text NOT NULL,
  `parent_id` text NULL,
  `root_id` text NOT NULL,
  `task_type` text NOT NULL DEFAULT 'task',
  `title` text NOT NULL,
  `description` text NOT NULL DEFAULT '',
  `status` text NOT NULL DEFAULT 'draft',
  `priority` text NOT NULL DEFAULT 'routine',
  `required` boolean NOT NULL DEFAULT 1,
  `retry_count` integer NOT NULL DEFAULT 0,
  `max_retries` integer NOT NULL DEFAULT 3,
  `review_policy` text NULL,
  `session_id` text NULL,
  `context` text NOT NULL DEFAULT '{}',
  `review_request` text NOT NULL DEFAULT '{}',
  `notify_at` text NULL,
  `scheduler_job_id` text NULL,
  `scheduler_run_id` text NULL,
  `assignee_agent_id` text NULL,
  `created_by_agent_id` text NULL,
  `user_id` text NOT NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`user_id`) REFERENCES `auth_user` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `1` FOREIGN KEY (`created_by_agent_id`) REFERENCES `settings_agent` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `2` FOREIGN KEY (`assignee_agent_id`) REFERENCES `settings_agent` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `3` FOREIGN KEY (`scheduler_run_id`) REFERENCES `sched_job_run` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `4` FOREIGN KEY (`scheduler_job_id`) REFERENCES `sched_job` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `5` FOREIGN KEY (`root_id`) REFERENCES `agent_task` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `6` FOREIGN KEY (`parent_id`) REFERENCES `agent_task` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CHECK (task_type IN ('goal','task')),
  CHECK (status IN ('draft','ready','running','blocked','reviewing','changes_requested','done','failed','cancelled')),
  CHECK (priority IN ('routine','urgent')),
  CHECK (review_policy IN ('auto','agent','human')),
  CHECK ((parent_id IS NULL) = (root_id = id))
);
-- Copy rows from old table "agent_task" to new temporary table "new_agent_task"
-- Data migration: map pending→ready, review_requested→reviewing, set root_id=id for all existing rows (they become root tasks)
INSERT INTO `new_agent_task` (`id`, `root_id`, `task_type`, `title`, `description`, `status`, `priority`, `session_id`, `context`, `review_request`, `notify_at`, `scheduler_job_id`, `scheduler_run_id`, `assignee_agent_id`, `user_id`, `created_at`, `updated_at`)
SELECT `id`, `id` AS `root_id`, 'task' AS `task_type`, `title`, `description`,
  CASE `status`
    WHEN 'pending' THEN 'ready'
    WHEN 'review_requested' THEN 'reviewing'
    ELSE `status`
  END AS `status`,
  `priority`, `session_id`, `context`, `review_request`, `notify_at`, `scheduler_job_id`, `scheduler_run_id`, `agent_id`, `user_id`, `created_at`, `updated_at`
FROM `agent_task`;
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
-- Create index "idx_agent_task_assignee_agent_id" to table: "agent_task"
CREATE INDEX `idx_agent_task_assignee_agent_id` ON `agent_task` (`assignee_agent_id`);
-- Create index "idx_agent_task_root_id" to table: "agent_task"
CREATE INDEX `idx_agent_task_root_id` ON `agent_task` (`root_id`);
-- Create index "idx_agent_task_parent_id" to table: "agent_task"
CREATE INDEX `idx_agent_task_parent_id` ON `agent_task` (`parent_id`);
-- Create index "idx_agent_task_type_status" to table: "agent_task"
CREATE INDEX `idx_agent_task_type_status` ON `agent_task` (`task_type`, `status`);
-- Enable back the enforcement of foreign-keys constraints
PRAGMA foreign_keys = on;
