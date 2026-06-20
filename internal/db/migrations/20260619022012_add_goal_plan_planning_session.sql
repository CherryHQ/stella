-- Disable the enforcement of foreign-keys constraints
PRAGMA foreign_keys = off;
-- Create "new_agent_goal_plan" table
CREATE TABLE `new_agent_goal_plan` (
  `id` text NOT NULL,
  `goal_id` text NOT NULL,
  `status` text NOT NULL DEFAULT 'draft',
  `review_policy` text NOT NULL DEFAULT 'none',
  `content_json` text NOT NULL DEFAULT '{}',
  `pending_content_json` text NULL,
  `source_run_id` text NULL,
  `approved_review_id` text NULL,
  `planning_session_id` text NULL,
  `accepted_at` text NULL,
  `materialized_at` text NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`planning_session_id`) REFERENCES `ctx_conversation` (`session_id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `1` FOREIGN KEY (`approved_review_id`) REFERENCES `agent_review` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `2` FOREIGN KEY (`source_run_id`) REFERENCES `agent_task_run` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `3` FOREIGN KEY (`goal_id`) REFERENCES `agent_goal` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Copy rows from old table "agent_goal_plan" to new temporary table "new_agent_goal_plan"
INSERT INTO `new_agent_goal_plan` (`id`, `goal_id`, `status`, `review_policy`, `content_json`, `pending_content_json`, `source_run_id`, `approved_review_id`, `accepted_at`, `materialized_at`, `created_at`, `updated_at`) SELECT `id`, `goal_id`, `status`, `review_policy`, `content_json`, `pending_content_json`, `source_run_id`, `approved_review_id`, `accepted_at`, `materialized_at`, `created_at`, `updated_at` FROM `agent_goal_plan`;
-- Drop "agent_goal_plan" table after copying rows
DROP TABLE `agent_goal_plan`;
-- Rename temporary table "new_agent_goal_plan" to "agent_goal_plan"
ALTER TABLE `new_agent_goal_plan` RENAME TO `agent_goal_plan`;
-- Create index "agent_goal_plan_goal_id" to table: "agent_goal_plan"
CREATE UNIQUE INDEX `agent_goal_plan_goal_id` ON `agent_goal_plan` (`goal_id`);
-- Create index "idx_agent_goal_plan_goal_status" to table: "agent_goal_plan"
CREATE INDEX `idx_agent_goal_plan_goal_status` ON `agent_goal_plan` (`goal_id`, `status`);
-- Create index "idx_agent_goal_plan_source_run" to table: "agent_goal_plan"
CREATE INDEX `idx_agent_goal_plan_source_run` ON `agent_goal_plan` (`source_run_id`);
-- Create index "idx_agent_goal_plan_approved_review" to table: "agent_goal_plan"
CREATE INDEX `idx_agent_goal_plan_approved_review` ON `agent_goal_plan` (`approved_review_id`);
-- Create index "idx_agent_goal_plan_planning_session" to table: "agent_goal_plan"
CREATE INDEX `idx_agent_goal_plan_planning_session` ON `agent_goal_plan` (`planning_session_id`);
-- Enable back the enforcement of foreign-keys constraints
PRAGMA foreign_keys = on;
