-- Disable the enforcement of foreign-keys constraints
PRAGMA foreign_keys = off;
-- Create "new_agent_task_event" table
CREATE TABLE `new_agent_task_event` (
  `id` text NOT NULL,
  `task_id` text NOT NULL,
  `run_id` text NULL,
  `review_id` text NULL,
  `event_type` text NOT NULL,
  `detail` text NOT NULL DEFAULT '{}',
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`review_id`) REFERENCES `agent_task_review` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `1` FOREIGN KEY (`run_id`) REFERENCES `agent_task_run` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `2` FOREIGN KEY (`task_id`) REFERENCES `agent_task` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Copy rows from old table "agent_task_event" to new temporary table "new_agent_task_event"
INSERT INTO `new_agent_task_event` (`id`, `task_id`, `event_type`, `detail`, `created_at`, `updated_at`) SELECT `id`, `task_id`, `event_type`, `detail`, `created_at`, `updated_at` FROM `agent_task_event`;
-- Drop "agent_task_event" table after copying rows
DROP TABLE `agent_task_event`;
-- Rename temporary table "new_agent_task_event" to "agent_task_event"
ALTER TABLE `new_agent_task_event` RENAME TO `agent_task_event`;
-- Create index "idx_agent_task_event_task_id" to table: "agent_task_event"
CREATE INDEX `idx_agent_task_event_task_id` ON `agent_task_event` (`task_id`, `created_at` DESC);
-- Create "agent_task_run" table
CREATE TABLE `agent_task_run` (
  `id` text NOT NULL,
  `user_id` text NOT NULL,
  `task_id` text NOT NULL,
  `agent_id` text NULL,
  `kind` text NOT NULL,
  `purpose` text NOT NULL,
  `status` text NOT NULL DEFAULT 'queued',
  `session_id` text NULL,
  `result_json` text NOT NULL DEFAULT '{}',
  `error` text NOT NULL DEFAULT '',
  `deadline_at` text NULL,
  `started_at` text NULL,
  `finished_at` text NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`agent_id`) REFERENCES `settings_agent` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `1` FOREIGN KEY (`task_id`) REFERENCES `agent_task` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `2` FOREIGN KEY (`user_id`) REFERENCES `auth_user` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CHECK (kind IN ('manager_run','worker_run','reviewer_run')),
  CHECK (purpose IN ('planning','synthesis','replan','execution','review','auto_approval','failure_assessment')),
  CHECK (status IN ('queued','running','completed','failed','cancelled','interrupted'))
);
-- Create index "idx_agent_task_run_user_id" to table: "agent_task_run"
CREATE INDEX `idx_agent_task_run_user_id` ON `agent_task_run` (`user_id`);
-- Create index "idx_agent_task_run_task_kind_status" to table: "agent_task_run"
CREATE INDEX `idx_agent_task_run_task_kind_status` ON `agent_task_run` (`task_id`, `kind`, `status`);
-- Create index "idx_agent_task_run_agent_id" to table: "agent_task_run"
CREATE INDEX `idx_agent_task_run_agent_id` ON `agent_task_run` (`agent_id`);
-- Create index "idx_agent_task_run_status" to table: "agent_task_run"
CREATE INDEX `idx_agent_task_run_status` ON `agent_task_run` (`status`);
-- Create "agent_task_acceptance_criterion" table
CREATE TABLE `agent_task_acceptance_criterion` (
  `id` text NOT NULL,
  `user_id` text NOT NULL,
  `task_id` text NOT NULL,
  `description` text NOT NULL,
  `required` boolean NOT NULL DEFAULT 1,
  `position` integer NOT NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`task_id`) REFERENCES `agent_task` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `1` FOREIGN KEY (`user_id`) REFERENCES `auth_user` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "agent_task_acceptance_criterion_task_id_position" to table: "agent_task_acceptance_criterion"
CREATE UNIQUE INDEX `agent_task_acceptance_criterion_task_id_position` ON `agent_task_acceptance_criterion` (`task_id`, `position`);
-- Create index "idx_agent_task_acceptance_criterion_task_id" to table: "agent_task_acceptance_criterion"
CREATE INDEX `idx_agent_task_acceptance_criterion_task_id` ON `agent_task_acceptance_criterion` (`task_id`);
-- Create "agent_task_review" table
CREATE TABLE `agent_task_review` (
  `id` text NOT NULL,
  `user_id` text NOT NULL,
  `task_id` text NOT NULL,
  `reviewer_type` text NOT NULL,
  `reviewer_id` text NOT NULL DEFAULT '',
  `submitted_run_id` text NOT NULL,
  `reviewer_run_id` text NULL,
  `status` text NOT NULL DEFAULT 'requested',
  `summary` text NOT NULL DEFAULT '',
  `feedback` text NOT NULL DEFAULT '',
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `resolved_at` text NULL,
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`reviewer_run_id`) REFERENCES `agent_task_run` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `1` FOREIGN KEY (`submitted_run_id`) REFERENCES `agent_task_run` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `2` FOREIGN KEY (`task_id`) REFERENCES `agent_task` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `3` FOREIGN KEY (`user_id`) REFERENCES `auth_user` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CHECK (reviewer_type IN ('agent','human','system')),
  CHECK (status IN ('requested','approved','changes_requested','rejected','cancelled'))
);
-- Create index "idx_agent_task_review_task_id" to table: "agent_task_review"
CREATE INDEX `idx_agent_task_review_task_id` ON `agent_task_review` (`task_id`);
-- Create index "idx_agent_task_review_status" to table: "agent_task_review"
CREATE INDEX `idx_agent_task_review_status` ON `agent_task_review` (`status`);
-- Create index "idx_agent_task_review_submitted_run_id" to table: "agent_task_review"
CREATE INDEX `idx_agent_task_review_submitted_run_id` ON `agent_task_review` (`submitted_run_id`);
-- Create "agent_task_review_item" table
CREATE TABLE `agent_task_review_item` (
  `id` text NOT NULL,
  `user_id` text NOT NULL,
  `review_id` text NOT NULL,
  `criterion_id` text NOT NULL,
  `passed` boolean NULL,
  `evidence` text NOT NULL DEFAULT '',
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`criterion_id`) REFERENCES `agent_task_acceptance_criterion` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `1` FOREIGN KEY (`review_id`) REFERENCES `agent_task_review` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `2` FOREIGN KEY (`user_id`) REFERENCES `auth_user` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "agent_task_review_item_review_id_criterion_id" to table: "agent_task_review_item"
CREATE UNIQUE INDEX `agent_task_review_item_review_id_criterion_id` ON `agent_task_review_item` (`review_id`, `criterion_id`);
-- Create index "idx_agent_task_review_item_review_id" to table: "agent_task_review_item"
CREATE INDEX `idx_agent_task_review_item_review_id` ON `agent_task_review_item` (`review_id`);
-- Enable back the enforcement of foreign-keys constraints
PRAGMA foreign_keys = on;
