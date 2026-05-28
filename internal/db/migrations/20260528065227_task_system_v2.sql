-- Disable the enforcement of foreign-keys constraints
PRAGMA foreign_keys = off;
-- Drop all rows from old task tables: D1 of plan.md decides the task system v2
-- replaces the v1 surface and does not migrate data. Old rows carry status
-- values ('pending','review_requested') that the new CHECK rejects; the v1
-- feature had no production users worth preserving.
DELETE FROM `agent_task_event`;
DELETE FROM `agent_task`;
-- Create "new_agent_task" table
CREATE TABLE `new_agent_task` (
  `id` text NOT NULL,
  `org_id` text NOT NULL,
  `user_id` text NOT NULL,
  `agent_id` text NULL,
  `title` text NOT NULL,
  `description` text NOT NULL DEFAULT '',
  `status` text NOT NULL DEFAULT 'draft',
  `priority` text NOT NULL DEFAULT 'routine',
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
  CONSTRAINT `2` FOREIGN KEY (`agent_id`) REFERENCES `settings_agent` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `3` FOREIGN KEY (`user_id`) REFERENCES `auth_user` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `4` FOREIGN KEY (`org_id`) REFERENCES `auth_organization` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CHECK (status IN (
                            'draft','ready','running','blocked','done','failed','cancelled'
                            -- Slice 2 widens to include 'reviewing'
                        )),
  CHECK (priority IN ('routine','urgent'))
);
-- Copy rows from old table "agent_task" to new temporary table "new_agent_task"
INSERT INTO `new_agent_task` (`id`, `user_id`, `agent_id`, `title`, `description`, `status`, `priority`, `session_id`, `context`, `created_at`, `updated_at`) SELECT `id`, `user_id`, `agent_id`, `title`, `description`, IFNULL(`status`, 'draft') AS `status`, `priority`, `session_id`, `context`, `created_at`, `updated_at` FROM `agent_task`;
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
-- Create "new_agent_task_event" table
CREATE TABLE `new_agent_task_event` (
  `id` text NOT NULL,
  `task_id` text NULL,
  `run_id` text NULL,
  `blocker_id` text NULL,
  `event_type` text NOT NULL,
  `from_status` text NULL,
  `to_status` text NULL,
  `actor_type` text NOT NULL,
  `actor_id` text NULL,
  `detail` text NOT NULL DEFAULT '{}',
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`blocker_id`) REFERENCES `agent_task_blocker` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `1` FOREIGN KEY (`run_id`) REFERENCES `agent_task_run` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `2` FOREIGN KEY (`task_id`) REFERENCES `agent_task` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CHECK (actor_type IN (
                        'system','user','agent','worker'
                        -- Slice 2: + 'reviewer'
                        -- Slice 3: + 'planner','synthesizer'
                    ))
);
-- Copy rows from old table "agent_task_event" to new temporary table "new_agent_task_event"
INSERT INTO `new_agent_task_event` (`id`, `task_id`, `event_type`, `detail`, `created_at`) SELECT `id`, `task_id`, `event_type`, `detail`, `created_at` FROM `agent_task_event`;
-- Drop "agent_task_event" table after copying rows
DROP TABLE `agent_task_event`;
-- Rename temporary table "new_agent_task_event" to "agent_task_event"
ALTER TABLE `new_agent_task_event` RENAME TO `agent_task_event`;
-- Create index "idx_agent_task_event_task" to table: "agent_task_event"
CREATE INDEX `idx_agent_task_event_task` ON `agent_task_event` (`task_id`, `created_at` DESC);
-- Create index "idx_agent_task_event_run" to table: "agent_task_event"
CREATE INDEX `idx_agent_task_event_run` ON `agent_task_event` (`run_id`);
-- Create "agent_task_run" table
CREATE TABLE `agent_task_run` (
  `id` text NOT NULL,
  `task_id` text NULL,
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
  CONSTRAINT `4` FOREIGN KEY (`task_id`) REFERENCES `agent_task` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CHECK (kind IN ('worker')),
  CHECK (status IN (
                            'queued','running','completed','failed','cancelled','interrupted','timed_out'
                        )),
  CHECK (task_id IS NOT NULL)
);
-- Create index "idx_agent_task_run_task" to table: "agent_task_run"
CREATE INDEX `idx_agent_task_run_task` ON `agent_task_run` (`task_id`, `attempt_no` DESC);
-- Create index "idx_agent_task_run_active" to table: "agent_task_run"
CREATE INDEX `idx_agent_task_run_active` ON `agent_task_run` (`status`) WHERE status IN ('queued','running');
-- Create index "idx_agent_task_run_lease" to table: "agent_task_run"
CREATE INDEX `idx_agent_task_run_lease` ON `agent_task_run` (`lease_expires_at`) WHERE status IN ('queued','running');
-- Create index "uniq_active_worker_run" to table: "agent_task_run"
CREATE UNIQUE INDEX `uniq_active_worker_run` ON `agent_task_run` (`task_id`) WHERE task_id IS NOT NULL AND kind = 'worker' AND status IN ('queued','running');
-- Create index "uniq_task_run_attempt" to table: "agent_task_run"
CREATE UNIQUE INDEX `uniq_task_run_attempt` ON `agent_task_run` (`task_id`, `kind`, `attempt_no`) WHERE task_id IS NOT NULL;
-- Create "agent_task_blocker" table
CREATE TABLE `agent_task_blocker` (
  `id` text NOT NULL,
  `task_id` text NOT NULL,
  `kind` text NOT NULL,
  `status` text NOT NULL DEFAULT 'open',
  `question` text NOT NULL DEFAULT '',
  `detail` text NOT NULL DEFAULT '{}',
  `resolution` text NOT NULL DEFAULT '{}',
  `created_by_run_id` text NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `resolved_at` text NULL,
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`created_by_run_id`) REFERENCES `agent_task_run` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `1` FOREIGN KEY (`task_id`) REFERENCES `agent_task` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CHECK (kind IN (
                            'user_input','external_dependency','tool_error','policy_hold','dep_failure'
                        )),
  CHECK (status IN ('open','resolved','cancelled'))
);
-- Create index "idx_agent_task_blocker_task_open" to table: "agent_task_blocker"
CREATE INDEX `idx_agent_task_blocker_task_open` ON `agent_task_blocker` (`task_id`) WHERE status='open';
-- Create index "uniq_open_blocker_per_task" to table: "agent_task_blocker"
CREATE UNIQUE INDEX `uniq_open_blocker_per_task` ON `agent_task_blocker` (`task_id`) WHERE status = 'open';
-- Create "agent_task_dep" table
CREATE TABLE `agent_task_dep` (
  `task_id` text NOT NULL,
  `dep_task_id` text NOT NULL,
  `dep_kind` text NOT NULL DEFAULT 'hard',
  `on_failure` text NOT NULL DEFAULT 'block',
  `waived_at` text NULL,
  `waived_by_user` text NULL,
  `waiver_reason` text NOT NULL DEFAULT '',
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`task_id`, `dep_task_id`),
  CONSTRAINT `0` FOREIGN KEY (`waived_by_user`) REFERENCES `auth_user` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `1` FOREIGN KEY (`dep_task_id`) REFERENCES `agent_task` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `2` FOREIGN KEY (`task_id`) REFERENCES `agent_task` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CHECK (dep_kind IN ('hard','soft')),
  CHECK (on_failure IN ('block','fail','ignore')),
  CHECK (task_id != dep_task_id)
);
-- Create index "idx_agent_task_dep_dep" to table: "agent_task_dep"
CREATE INDEX `idx_agent_task_dep_dep` ON `agent_task_dep` (`dep_task_id`);
-- Create "agent_task_dispatch_hint" table
CREATE TABLE `agent_task_dispatch_hint` (
  `id` text NOT NULL,
  `task_id` text NOT NULL,
  `kind` text NOT NULL,
  `executor_agent_id` text NOT NULL,
  `consumed_at` text NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`executor_agent_id`) REFERENCES `settings_agent` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `1` FOREIGN KEY (`task_id`) REFERENCES `agent_task` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CHECK (kind IN ('worker'))
);
-- Create index "uniq_active_dispatch_hint_task" to table: "agent_task_dispatch_hint"
CREATE UNIQUE INDEX `uniq_active_dispatch_hint_task` ON `agent_task_dispatch_hint` (`task_id`, `kind`) WHERE consumed_at IS NULL;
-- Enable back the enforcement of foreign-keys constraints
PRAGMA foreign_keys = on;
