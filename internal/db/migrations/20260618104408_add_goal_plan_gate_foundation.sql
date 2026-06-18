-- Disable the enforcement of foreign-keys constraints
PRAGMA foreign_keys = off;
-- Add column "subject" to table: "agent_review"
ALTER TABLE `agent_review` ADD COLUMN `subject` text NOT NULL DEFAULT 'completion';
-- Create "new_agent_task" table
CREATE TABLE `new_agent_task` (
  `id` text NOT NULL,
  `user_id` text NOT NULL,
  `agent_id` text NOT NULL,
  `session_id` text NOT NULL,
  `goal_id` text NULL,
  `project_id` text NULL,
  `source_plan_id` text NULL,
  `plan_item_id` text NOT NULL DEFAULT '',
  `detached_at` text NULL,
  `title` text NOT NULL,
  `description` text NOT NULL DEFAULT '',
  `status` text NOT NULL DEFAULT 'draft',
  `priority` text NOT NULL DEFAULT 'routine',
  `review_policy` text NOT NULL DEFAULT 'none',
  `active_review_id` text NULL,
  `required` integer NOT NULL DEFAULT 1,
  `retry_count` integer NOT NULL DEFAULT 0,
  `max_retries` integer NOT NULL DEFAULT 3,
  `not_before` text NULL,
  `deadline_at` text NULL,
  `active_run_id` text NULL,
  `active_blocker_id` text NULL,
  `context` text NOT NULL DEFAULT '{}',
  `output` text NOT NULL DEFAULT '{}',
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  `completed_at` text NULL,
  `cancelled_at` text NULL,
  `archived_at` text NULL,
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`active_blocker_id`) REFERENCES `agent_task_blocker` (`id`) ON UPDATE NO ACTION ON DELETE RESTRICT,
  CONSTRAINT `1` FOREIGN KEY (`active_run_id`) REFERENCES `agent_task_run` (`id`) ON UPDATE NO ACTION ON DELETE RESTRICT,
  CONSTRAINT `2` FOREIGN KEY (`active_review_id`) REFERENCES `agent_review` (`id`) ON UPDATE NO ACTION ON DELETE RESTRICT,
  CONSTRAINT `3` FOREIGN KEY (`source_plan_id`) REFERENCES `agent_goal_plan` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `4` FOREIGN KEY (`project_id`) REFERENCES `project` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `5` FOREIGN KEY (`goal_id`) REFERENCES `agent_goal` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `6` FOREIGN KEY (`session_id`) REFERENCES `ctx_conversation` (`session_id`) ON UPDATE NO ACTION ON DELETE RESTRICT,
  CONSTRAINT `7` FOREIGN KEY (`agent_id`) REFERENCES `agent` (`id`) ON UPDATE NO ACTION ON DELETE RESTRICT,
  CONSTRAINT `8` FOREIGN KEY (`user_id`) REFERENCES `auth_user` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Copy rows from old table "agent_task" to new temporary table "new_agent_task"
INSERT INTO `new_agent_task` (`id`, `user_id`, `agent_id`, `session_id`, `goal_id`, `project_id`, `title`, `description`, `status`, `priority`, `review_policy`, `active_review_id`, `required`, `retry_count`, `max_retries`, `not_before`, `deadline_at`, `active_run_id`, `active_blocker_id`, `context`, `output`, `created_at`, `updated_at`, `completed_at`, `cancelled_at`, `archived_at`) SELECT `id`, `user_id`, `agent_id`, `session_id`, `goal_id`, `project_id`, `title`, `description`, `status`, `priority`, `review_policy`, `active_review_id`, `required`, `retry_count`, `max_retries`, `not_before`, `deadline_at`, `active_run_id`, `active_blocker_id`, `context`, `output`, `created_at`, `updated_at`, `completed_at`, `cancelled_at`, `archived_at` FROM `agent_task`;
-- Drop "agent_task" table after copying rows
DROP TABLE `agent_task`;
-- Rename temporary table "new_agent_task" to "agent_task"
ALTER TABLE `new_agent_task` RENAME TO `agent_task`;
-- Create index "uniq_agent_task_session" to table: "agent_task"
CREATE UNIQUE INDEX `uniq_agent_task_session` ON `agent_task` (`session_id`);
-- Create index "idx_agent_task_agent_status_not_before" to table: "agent_task"
CREATE INDEX `idx_agent_task_agent_status_not_before` ON `agent_task` (`agent_id`, `status`, `not_before`);
-- Create index "idx_agent_task_user_agent" to table: "agent_task"
CREATE INDEX `idx_agent_task_user_agent` ON `agent_task` (`user_id`, `agent_id`);
-- Create index "idx_agent_task_user_created" to table: "agent_task"
CREATE INDEX `idx_agent_task_user_created` ON `agent_task` (`user_id`, `created_at` DESC, `id` DESC);
-- Create index "idx_agent_task_user_archived_created" to table: "agent_task"
CREATE INDEX `idx_agent_task_user_archived_created` ON `agent_task` (`user_id`, `archived_at`, `created_at` DESC, `id` DESC);
-- Create index "idx_agent_task_user_agent_status_created" to table: "agent_task"
CREATE INDEX `idx_agent_task_user_agent_status_created` ON `agent_task` (`user_id`, `agent_id`, `status`, `created_at` DESC, `id` DESC);
-- Create index "idx_agent_task_user_agent_project_created" to table: "agent_task"
CREATE INDEX `idx_agent_task_user_agent_project_created` ON `agent_task` (`user_id`, `agent_id`, `project_id`, `created_at` DESC, `id` DESC);
-- Create index "idx_agent_task_ready_candidates" to table: "agent_task"
CREATE INDEX `idx_agent_task_ready_candidates` ON `agent_task` (`priority` DESC, `created_at`) WHERE status = 'ready' AND active_run_id IS NULL;
-- Create index "idx_agent_task_session" to table: "agent_task"
CREATE INDEX `idx_agent_task_session` ON `agent_task` (`session_id`);
-- Create index "idx_agent_task_goal" to table: "agent_task"
CREATE INDEX `idx_agent_task_goal` ON `agent_task` (`goal_id`);
-- Create index "idx_agent_task_project" to table: "agent_task"
CREATE INDEX `idx_agent_task_project` ON `agent_task` (`project_id`);
-- Create index "uniq_agent_task_source_plan_item" to table: "agent_task"
CREATE UNIQUE INDEX `uniq_agent_task_source_plan_item` ON `agent_task` (`source_plan_id`, `plan_item_id`) WHERE source_plan_id IS NOT NULL AND plan_item_id != '';
-- Create "agent_goal_plan" table
CREATE TABLE `agent_goal_plan` (
  `id` text NOT NULL,
  `goal_id` text NOT NULL,
  `status` text NOT NULL DEFAULT 'draft',
  `review_policy` text NOT NULL DEFAULT 'none',
  `content_json` text NOT NULL DEFAULT '{}',
  `pending_content_json` text NULL,
  `source_run_id` text NULL,
  `approved_review_id` text NULL,
  `accepted_at` text NULL,
  `materialized_at` text NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`approved_review_id`) REFERENCES `agent_review` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `1` FOREIGN KEY (`source_run_id`) REFERENCES `agent_task_run` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `2` FOREIGN KEY (`goal_id`) REFERENCES `agent_goal` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "agent_goal_plan_goal_id" to table: "agent_goal_plan"
CREATE UNIQUE INDEX `agent_goal_plan_goal_id` ON `agent_goal_plan` (`goal_id`);
-- Create index "idx_agent_goal_plan_goal_status" to table: "agent_goal_plan"
CREATE INDEX `idx_agent_goal_plan_goal_status` ON `agent_goal_plan` (`goal_id`, `status`);
-- Enable back the enforcement of foreign-keys constraints
PRAGMA foreign_keys = on;
