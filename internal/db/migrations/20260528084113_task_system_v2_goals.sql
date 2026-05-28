-- Disable the enforcement of foreign-keys constraints
PRAGMA foreign_keys = off;
-- Create "new_agent_task" table
CREATE TABLE `new_agent_task` (
  `id` text NOT NULL,
  `org_id` text NOT NULL,
  `user_id` text NOT NULL,
  `agent_id` text NULL,
  `goal_id` text NULL,
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
  `session_id` text NULL,
  `active_run_id` text NULL,
  `active_blocker_id` text NULL,
  `context` text NOT NULL DEFAULT '{}',
  `output` text NOT NULL DEFAULT '{}',
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  `completed_at` text NULL,
  `cancelled_at` text NULL,
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`active_blocker_id`) REFERENCES `agent_task_blocker` (`id`) ON UPDATE NO ACTION ON DELETE RESTRICT,
  CONSTRAINT `1` FOREIGN KEY (`active_run_id`) REFERENCES `agent_task_run` (`id`) ON UPDATE NO ACTION ON DELETE RESTRICT,
  CONSTRAINT `2` FOREIGN KEY (`active_review_id`) REFERENCES `agent_review` (`id`) ON UPDATE NO ACTION ON DELETE RESTRICT,
  CONSTRAINT `3` FOREIGN KEY (`goal_id`) REFERENCES `agent_goal` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `4` FOREIGN KEY (`agent_id`) REFERENCES `settings_agent` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `5` FOREIGN KEY (`user_id`) REFERENCES `auth_user` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `6` FOREIGN KEY (`org_id`) REFERENCES `auth_organization` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CHECK (status IN ('draft','ready','running','blocked','reviewing','done','failed','cancelled')),
  CHECK (priority IN ('routine','urgent')),
  CHECK (review_policy IN ('none','auto','agent','human'))
);
-- Copy rows from old table "agent_task" to new temporary table "new_agent_task"
INSERT INTO `new_agent_task` (`id`, `org_id`, `user_id`, `agent_id`, `title`, `description`, `status`, `priority`, `review_policy`, `active_review_id`, `required`, `retry_count`, `max_retries`, `not_before`, `deadline_at`, `session_id`, `active_run_id`, `active_blocker_id`, `context`, `output`, `created_at`, `updated_at`, `completed_at`, `cancelled_at`) SELECT `id`, `org_id`, `user_id`, `agent_id`, `title`, `description`, `status`, `priority`, `review_policy`, `active_review_id`, `required`, `retry_count`, `max_retries`, `not_before`, `deadline_at`, `session_id`, `active_run_id`, `active_blocker_id`, `context`, `output`, `created_at`, `updated_at`, `completed_at`, `cancelled_at` FROM `agent_task`;
-- Drop "agent_task" table after copying rows
DROP TABLE `agent_task`;
-- Rename temporary table "new_agent_task" to "agent_task"
ALTER TABLE `new_agent_task` RENAME TO `agent_task`;
-- Create index "idx_agent_task_org_status" to table: "agent_task"
CREATE INDEX `idx_agent_task_org_status` ON `agent_task` (`org_id`, `status`);
-- Create index "idx_agent_task_status_not_before" to table: "agent_task"
CREATE INDEX `idx_agent_task_status_not_before` ON `agent_task` (`status`, `not_before`);
-- Create index "idx_agent_task_session" to table: "agent_task"
CREATE INDEX `idx_agent_task_session` ON `agent_task` (`session_id`);
-- Create index "idx_agent_task_goal" to table: "agent_task"
CREATE INDEX `idx_agent_task_goal` ON `agent_task` (`goal_id`);
-- Create "new_agent_task_event" table
CREATE TABLE `new_agent_task_event` (
  `id` text NOT NULL,
  `task_id` text NULL,
  `goal_id` text NULL,
  `run_id` text NULL,
  `blocker_id` text NULL,
  `review_id` text NULL,
  `event_type` text NOT NULL,
  `from_status` text NULL,
  `to_status` text NULL,
  `actor_type` text NOT NULL,
  `actor_id` text NULL,
  `detail` text NOT NULL DEFAULT '{}',
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`review_id`) REFERENCES `agent_review` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `1` FOREIGN KEY (`blocker_id`) REFERENCES `agent_task_blocker` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `2` FOREIGN KEY (`run_id`) REFERENCES `agent_task_run` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `3` FOREIGN KEY (`goal_id`) REFERENCES `agent_goal` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `4` FOREIGN KEY (`task_id`) REFERENCES `agent_task` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CHECK (actor_type IN ('system','user','agent','worker','reviewer','planner','synthesizer'))
);
-- Copy rows from old table "agent_task_event" to new temporary table "new_agent_task_event"
INSERT INTO `new_agent_task_event` (`id`, `task_id`, `run_id`, `blocker_id`, `review_id`, `event_type`, `from_status`, `to_status`, `actor_type`, `actor_id`, `detail`, `created_at`) SELECT `id`, `task_id`, `run_id`, `blocker_id`, `review_id`, `event_type`, `from_status`, `to_status`, `actor_type`, `actor_id`, `detail`, `created_at` FROM `agent_task_event`;
-- Drop "agent_task_event" table after copying rows
DROP TABLE `agent_task_event`;
-- Rename temporary table "new_agent_task_event" to "agent_task_event"
ALTER TABLE `new_agent_task_event` RENAME TO `agent_task_event`;
-- Create index "idx_agent_task_event_task" to table: "agent_task_event"
CREATE INDEX `idx_agent_task_event_task` ON `agent_task_event` (`task_id`, `created_at` DESC);
-- Create index "idx_agent_task_event_goal" to table: "agent_task_event"
CREATE INDEX `idx_agent_task_event_goal` ON `agent_task_event` (`goal_id`, `created_at` DESC);
-- Create index "idx_agent_task_event_run" to table: "agent_task_event"
CREATE INDEX `idx_agent_task_event_run` ON `agent_task_event` (`run_id`);
-- Create "new_agent_task_run" table
CREATE TABLE `new_agent_task_run` (
  `id` text NOT NULL,
  `task_id` text NULL,
  `goal_id` text NULL,
  `org_id` text NOT NULL,
  `user_id` text NOT NULL,
  `agent_id` text NULL,
  `executor_agent_id` text NULL,
  `kind` text NOT NULL DEFAULT 'worker',
  `attempt_no` integer NOT NULL DEFAULT 1,
  `status` text NOT NULL DEFAULT 'queued',
  `session_id` text NOT NULL,
  `input` text NOT NULL DEFAULT '{}',
  `result` text NOT NULL DEFAULT '{}',
  `error` text NOT NULL DEFAULT '',
  `heartbeat_at` text NULL,
  `lease_expires_at` text NULL,
  `worker_id` text NOT NULL DEFAULT '',
  `started_at` text NULL,
  `finished_at` text NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`executor_agent_id`) REFERENCES `settings_agent` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `1` FOREIGN KEY (`agent_id`) REFERENCES `settings_agent` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `2` FOREIGN KEY (`user_id`) REFERENCES `auth_user` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `3` FOREIGN KEY (`org_id`) REFERENCES `auth_organization` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `4` FOREIGN KEY (`goal_id`) REFERENCES `agent_goal` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `5` FOREIGN KEY (`task_id`) REFERENCES `agent_task` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CHECK (kind IN ('worker','reviewer','planner','synthesizer')),
  CHECK (status IN ('queued','running','completed','failed','cancelled','interrupted','timed_out')),
  CHECK (
      (task_id IS NOT NULL AND goal_id IS NULL     AND kind IN ('worker','reviewer'))
      OR
      (task_id IS NULL     AND goal_id IS NOT NULL AND kind IN ('planner','synthesizer'))
    )
);
-- Copy rows from old table "agent_task_run" to new temporary table "new_agent_task_run"
INSERT INTO `new_agent_task_run` (`id`, `task_id`, `org_id`, `user_id`, `agent_id`, `executor_agent_id`, `kind`, `attempt_no`, `status`, `session_id`, `input`, `result`, `error`, `heartbeat_at`, `lease_expires_at`, `worker_id`, `started_at`, `finished_at`, `created_at`, `updated_at`) SELECT `id`, `task_id`, `org_id`, `user_id`, `agent_id`, `executor_agent_id`, `kind`, `attempt_no`, `status`, `session_id`, `input`, `result`, `error`, `heartbeat_at`, `lease_expires_at`, `worker_id`, `started_at`, `finished_at`, `created_at`, `updated_at` FROM `agent_task_run`;
-- Drop "agent_task_run" table after copying rows
DROP TABLE `agent_task_run`;
-- Rename temporary table "new_agent_task_run" to "agent_task_run"
ALTER TABLE `new_agent_task_run` RENAME TO `agent_task_run`;
-- Create index "idx_agent_task_run_task" to table: "agent_task_run"
CREATE INDEX `idx_agent_task_run_task` ON `agent_task_run` (`task_id`, `attempt_no` DESC);
-- Create index "idx_agent_task_run_active" to table: "agent_task_run"
CREATE INDEX `idx_agent_task_run_active` ON `agent_task_run` (`status`) WHERE status IN ('queued','running');
-- Create index "idx_agent_task_run_lease" to table: "agent_task_run"
CREATE INDEX `idx_agent_task_run_lease` ON `agent_task_run` (`lease_expires_at`) WHERE status IN ('queued','running');
-- Create index "uniq_active_worker_run" to table: "agent_task_run"
CREATE UNIQUE INDEX `uniq_active_worker_run` ON `agent_task_run` (`task_id`) WHERE task_id IS NOT NULL AND kind = 'worker' AND status IN ('queued','running');
-- Create index "uniq_active_reviewer_run" to table: "agent_task_run"
CREATE UNIQUE INDEX `uniq_active_reviewer_run` ON `agent_task_run` (`task_id`) WHERE task_id IS NOT NULL AND kind = 'reviewer' AND status IN ('queued','running');
-- Create index "uniq_active_planner_run" to table: "agent_task_run"
CREATE UNIQUE INDEX `uniq_active_planner_run` ON `agent_task_run` (`goal_id`) WHERE goal_id IS NOT NULL AND kind = 'planner' AND status IN ('queued','running');
-- Create index "uniq_active_synthesizer_run" to table: "agent_task_run"
CREATE UNIQUE INDEX `uniq_active_synthesizer_run` ON `agent_task_run` (`goal_id`) WHERE goal_id IS NOT NULL AND kind = 'synthesizer' AND status IN ('queued','running');
-- Create index "idx_agent_task_run_goal" to table: "agent_task_run"
CREATE INDEX `idx_agent_task_run_goal` ON `agent_task_run` (`goal_id`);
-- Create index "uniq_goal_run_attempt" to table: "agent_task_run"
CREATE UNIQUE INDEX `uniq_goal_run_attempt` ON `agent_task_run` (`goal_id`, `kind`, `attempt_no`) WHERE goal_id IS NOT NULL;
-- Create index "uniq_task_run_attempt" to table: "agent_task_run"
CREATE UNIQUE INDEX `uniq_task_run_attempt` ON `agent_task_run` (`task_id`, `kind`, `attempt_no`) WHERE task_id IS NOT NULL;
-- Create "new_agent_task_dispatch_hint" table
CREATE TABLE `new_agent_task_dispatch_hint` (
  `id` text NOT NULL,
  `task_id` text NULL,
  `goal_id` text NULL,
  `kind` text NOT NULL,
  `executor_agent_id` text NOT NULL,
  `consumed_at` text NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`executor_agent_id`) REFERENCES `settings_agent` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `1` FOREIGN KEY (`goal_id`) REFERENCES `agent_goal` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `2` FOREIGN KEY (`task_id`) REFERENCES `agent_task` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CHECK (kind IN ('worker','reviewer','planner','synthesizer')),
  CHECK (
      (task_id IS NOT NULL AND goal_id IS NULL     AND kind IN ('worker','reviewer'))
      OR
      (task_id IS NULL     AND goal_id IS NOT NULL AND kind IN ('planner','synthesizer'))
    )
);
-- Copy rows from old table "agent_task_dispatch_hint" to new temporary table "new_agent_task_dispatch_hint"
INSERT INTO `new_agent_task_dispatch_hint` (`id`, `task_id`, `kind`, `executor_agent_id`, `consumed_at`, `created_at`) SELECT `id`, `task_id`, `kind`, `executor_agent_id`, `consumed_at`, `created_at` FROM `agent_task_dispatch_hint`;
-- Drop "agent_task_dispatch_hint" table after copying rows
DROP TABLE `agent_task_dispatch_hint`;
-- Rename temporary table "new_agent_task_dispatch_hint" to "agent_task_dispatch_hint"
ALTER TABLE `new_agent_task_dispatch_hint` RENAME TO `agent_task_dispatch_hint`;
-- Create index "uniq_active_dispatch_hint_task" to table: "agent_task_dispatch_hint"
CREATE UNIQUE INDEX `uniq_active_dispatch_hint_task` ON `agent_task_dispatch_hint` (`task_id`, `kind`) WHERE task_id IS NOT NULL AND consumed_at IS NULL;
-- Create index "uniq_active_dispatch_hint_goal" to table: "agent_task_dispatch_hint"
CREATE UNIQUE INDEX `uniq_active_dispatch_hint_goal` ON `agent_task_dispatch_hint` (`goal_id`, `kind`) WHERE goal_id IS NOT NULL AND consumed_at IS NULL;
-- Create "agent_goal" table
CREATE TABLE `agent_goal` (
  `id` text NOT NULL,
  `org_id` text NOT NULL,
  `user_id` text NOT NULL,
  `agent_id` text NULL,
  `title` text NOT NULL,
  `description` text NOT NULL DEFAULT '',
  `status` text NOT NULL DEFAULT 'draft',
  `priority` text NOT NULL DEFAULT 'routine',
  `review_policy` text NOT NULL DEFAULT 'none',
  `active_review_id` text NULL,
  `context` text NOT NULL DEFAULT '{}',
  `output` text NOT NULL DEFAULT '{}',
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  `completed_at` text NULL,
  `cancelled_at` text NULL,
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`active_review_id`) REFERENCES `agent_review` (`id`) ON UPDATE NO ACTION ON DELETE RESTRICT,
  CONSTRAINT `1` FOREIGN KEY (`agent_id`) REFERENCES `settings_agent` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `2` FOREIGN KEY (`user_id`) REFERENCES `auth_user` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `3` FOREIGN KEY (`org_id`) REFERENCES `auth_organization` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CHECK (status IN ('draft','planning','running','blocked','reviewing','done','failed','cancelled')),
  CHECK (priority IN ('routine','urgent')),
  CHECK (review_policy IN ('none','auto','agent','human'))
);
-- Create index "idx_agent_goal_org_status" to table: "agent_goal"
CREATE INDEX `idx_agent_goal_org_status` ON `agent_goal` (`org_id`, `status`);
-- Enable back the enforcement of foreign-keys constraints
PRAGMA foreign_keys = on;
