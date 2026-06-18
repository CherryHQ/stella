-- Disable the enforcement of foreign-keys constraints
PRAGMA foreign_keys = off;
-- Create "new_agent_review" table
CREATE TABLE `new_agent_review` (
  `id` text NOT NULL,
  `task_id` text NULL,
  `goal_id` text NULL,
  `submitted_run_id` text NULL,
  `reviewer_run_id` text NULL,
  `reviewer_type` text NOT NULL,
  `reviewer_user_id` text NULL,
  `escalated_from_review_id` text NULL,
  `status` text NOT NULL DEFAULT 'requested',
  `subject` text NOT NULL DEFAULT 'completion',
  `summary` text NOT NULL DEFAULT '',
  `feedback` text NOT NULL DEFAULT '',
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  `resolved_at` text NULL,
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`escalated_from_review_id`) REFERENCES `agent_review` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `1` FOREIGN KEY (`reviewer_user_id`) REFERENCES `auth_user` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `2` FOREIGN KEY (`reviewer_run_id`) REFERENCES `agent_task_run` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `3` FOREIGN KEY (`submitted_run_id`) REFERENCES `agent_task_run` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `4` FOREIGN KEY (`goal_id`) REFERENCES `agent_goal` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `5` FOREIGN KEY (`task_id`) REFERENCES `agent_task` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CHECK (
      (task_id IS NOT NULL AND goal_id IS NULL)
      OR
      (task_id IS NULL AND goal_id IS NOT NULL)
    )
);
-- Copy rows from old table "agent_review" to new temporary table "new_agent_review"
INSERT INTO `new_agent_review` (`id`, `task_id`, `goal_id`, `submitted_run_id`, `reviewer_run_id`, `reviewer_type`, `reviewer_user_id`, `escalated_from_review_id`, `status`, `subject`, `summary`, `feedback`, `created_at`, `updated_at`, `resolved_at`) SELECT `id`, `task_id`, `goal_id`, `submitted_run_id`, `reviewer_run_id`, `reviewer_type`, `reviewer_user_id`, `escalated_from_review_id`, `status`, `subject`, `summary`, `feedback`, `created_at`, `updated_at`, `resolved_at` FROM `agent_review`;
-- Drop "agent_review" table after copying rows
DROP TABLE `agent_review`;
-- Rename temporary table "new_agent_review" to "agent_review"
ALTER TABLE `new_agent_review` RENAME TO `agent_review`;
-- Create index "idx_agent_review_task" to table: "agent_review"
CREATE INDEX `idx_agent_review_task` ON `agent_review` (`task_id`, `created_at` DESC);
-- Create index "idx_agent_review_open" to table: "agent_review"
CREATE INDEX `idx_agent_review_open` ON `agent_review` (`status`) WHERE status IN ('requested','in_progress');
-- Create index "uniq_open_review_per_task" to table: "agent_review"
CREATE UNIQUE INDEX `uniq_open_review_per_task` ON `agent_review` (`task_id`) WHERE task_id IS NOT NULL AND status IN ('requested','in_progress');
-- Create index "uniq_open_review_per_goal" to table: "agent_review"
CREATE UNIQUE INDEX `uniq_open_review_per_goal` ON `agent_review` (`goal_id`, `subject`) WHERE goal_id IS NOT NULL AND status IN ('requested','in_progress');
-- Enable back the enforcement of foreign-keys constraints
PRAGMA foreign_keys = on;
