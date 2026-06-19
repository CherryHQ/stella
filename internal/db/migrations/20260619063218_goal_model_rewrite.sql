-- Disable the enforcement of foreign-keys constraints
PRAGMA foreign_keys = off;
-- Drop "agent_task_run" table
DROP TABLE `agent_task_run`;
-- Drop "agent_task_blocker" table
DROP TABLE `agent_task_blocker`;
-- Drop "agent_task_criterion" table
DROP TABLE `agent_task_criterion`;
-- Drop "agent_review_item" table
DROP TABLE `agent_review_item`;
-- Drop "agent_task_dep" table
DROP TABLE `agent_task_dep`;
-- Drop "agent_task_dispatch_hint" table
DROP TABLE `agent_task_dispatch_hint`;
-- Drop "agent_task_event" table
DROP TABLE `agent_task_event`;
-- Drop "agent_goal" table
DROP TABLE `agent_goal`;
-- Drop "agent_review" table
DROP TABLE `agent_review`;
-- Drop "agent_task" table
DROP TABLE `agent_task`;
-- Drop "agent_goal_plan" table
DROP TABLE `agent_goal_plan`;
-- Create "agent_goal" table
CREATE TABLE `agent_goal` (
  `id` text NOT NULL,
  `user_id` text NOT NULL,
  `agent_id` text NOT NULL,
  `project_id` text NULL,
  `parent_id` text NULL,
  `root_id` text NOT NULL,
  `depth` integer NOT NULL DEFAULT 0,
  `position` integer NOT NULL DEFAULT 0,
  `session_id` text NOT NULL,
  `title` text NOT NULL,
  `intent` text NOT NULL DEFAULT '',
  `kind` text NOT NULL DEFAULT 'leaf',
  `priority` text NOT NULL DEFAULT 'routine',
  `required` integer NOT NULL DEFAULT 1,
  `acceptance_contract` text NOT NULL DEFAULT '{}',
  `convergence_policy` text NOT NULL DEFAULT '{}',
  `review_policy` text NOT NULL DEFAULT 'none',
  `lifecycle` text NOT NULL DEFAULT 'draft',
  `block_reason` text NOT NULL DEFAULT '',
  `acceptance_state` text NOT NULL DEFAULT 'pending',
  `accepted_output` text NULL,
  `acceptance_seq` integer NOT NULL DEFAULT 0,
  `active_attempt_id` text NULL,
  `attempt_count` integer NOT NULL DEFAULT 0,
  `required_total` integer NOT NULL DEFAULT 0,
  `required_accepted` integer NOT NULL DEFAULT 0,
  `required_failed` integer NOT NULL DEFAULT 0,
  `required_blocked` integer NOT NULL DEFAULT 0,
  `accepted_revision_id` text NULL,
  `context` text NOT NULL DEFAULT '{}',
  `dispatch_hint` text NOT NULL DEFAULT '{}',
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  `accepted_at` text NULL,
  `cancelled_at` text NULL,
  `archived_at` text NULL,
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`accepted_revision_id`) REFERENCES `agent_goal_revision` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `1` FOREIGN KEY (`active_attempt_id`) REFERENCES `agent_goal_attempt` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `2` FOREIGN KEY (`session_id`) REFERENCES `ctx_conversation` (`session_id`) ON UPDATE NO ACTION ON DELETE RESTRICT,
  CONSTRAINT `3` FOREIGN KEY (`root_id`) REFERENCES `agent_goal` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `4` FOREIGN KEY (`parent_id`) REFERENCES `agent_goal` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `5` FOREIGN KEY (`project_id`) REFERENCES `project` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `6` FOREIGN KEY (`agent_id`) REFERENCES `agent` (`id`) ON UPDATE NO ACTION ON DELETE RESTRICT,
  CONSTRAINT `7` FOREIGN KEY (`user_id`) REFERENCES `auth_user` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CHECK (parent_id IS NULL OR parent_id != id),
  CHECK (parent_id IS NOT NULL OR root_id = id),
  CHECK (required IN (0,1)),
  CHECK (depth >= 0 AND attempt_count >= 0),
  CHECK (required_total >= 0 AND required_accepted >= 0
           AND required_failed >= 0 AND required_blocked >= 0),
  CHECK (lifecycle != 'accepted'
           OR (acceptance_state = 'passed' AND accepted_output IS NOT NULL))
);
-- Create index "idx_agent_goal_parent" to table: "agent_goal"
CREATE INDEX `idx_agent_goal_parent` ON `agent_goal` (`parent_id`, `position`);
-- Create index "idx_agent_goal_root" to table: "agent_goal"
CREATE INDEX `idx_agent_goal_root` ON `agent_goal` (`root_id`);
-- Create index "idx_agent_goal_user_created" to table: "agent_goal"
CREATE INDEX `idx_agent_goal_user_created` ON `agent_goal` (`user_id`, `created_at` DESC, `id` DESC);
-- Create index "idx_agent_goal_user_archived_created" to table: "agent_goal"
CREATE INDEX `idx_agent_goal_user_archived_created` ON `agent_goal` (`user_id`, `archived_at`, `created_at` DESC, `id` DESC);
-- Create index "idx_agent_goal_agent_project" to table: "agent_goal"
CREATE INDEX `idx_agent_goal_agent_project` ON `agent_goal` (`agent_id`, `project_id`);
-- Create index "idx_agent_goal_project" to table: "agent_goal"
CREATE INDEX `idx_agent_goal_project` ON `agent_goal` (`project_id`);
-- Create index "idx_agent_goal_active_attempt" to table: "agent_goal"
CREATE INDEX `idx_agent_goal_active_attempt` ON `agent_goal` (`active_attempt_id`);
-- Create index "idx_agent_goal_accepted_revision" to table: "agent_goal"
CREATE INDEX `idx_agent_goal_accepted_revision` ON `agent_goal` (`accepted_revision_id`);
-- Create index "uniq_agent_goal_session" to table: "agent_goal"
CREATE UNIQUE INDEX `uniq_agent_goal_session` ON `agent_goal` (`session_id`);
-- Create index "idx_agent_goal_dispatchable" to table: "agent_goal"
CREATE INDEX `idx_agent_goal_dispatchable` ON `agent_goal` (`priority` DESC, `created_at`) WHERE lifecycle = 'ready' AND active_attempt_id IS NULL AND kind = 'leaf';
-- Create index "idx_agent_goal_rollup_candidates" to table: "agent_goal"
CREATE INDEX `idx_agent_goal_rollup_candidates` ON `agent_goal` (`root_id`, `updated_at`) WHERE kind = 'composite' AND lifecycle = 'active';
-- Create "agent_goal_attempt" table
CREATE TABLE `agent_goal_attempt` (
  `id` text NOT NULL,
  `goal_id` text NOT NULL,
  `user_id` text NOT NULL,
  `agent_id` text NULL,
  `executor_agent_id` text NULL,
  `session_id` text NOT NULL,
  `purpose` text NOT NULL DEFAULT 'execution',
  `attempt_no` integer NOT NULL DEFAULT 1,
  `status` text NOT NULL DEFAULT 'queued',
  `input_context` text NOT NULL DEFAULT '{}',
  `evidence` text NOT NULL DEFAULT '{}',
  `output` text NOT NULL DEFAULT '{}',
  `revision_id` text NULL,
  `gaps` text NOT NULL DEFAULT '{}',
  `error` text NOT NULL DEFAULT '',
  `heartbeat_at` text NULL,
  `lease_expires_at` text NULL,
  `worker_id` text NOT NULL DEFAULT '',
  `started_at` text NULL,
  `finished_at` text NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`revision_id`) REFERENCES `agent_goal_revision` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `1` FOREIGN KEY (`executor_agent_id`) REFERENCES `agent` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `2` FOREIGN KEY (`agent_id`) REFERENCES `agent` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `3` FOREIGN KEY (`user_id`) REFERENCES `auth_user` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `4` FOREIGN KEY (`goal_id`) REFERENCES `agent_goal` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CHECK (attempt_no >= 1)
);
-- Create index "idx_agent_goal_attempt_goal" to table: "agent_goal_attempt"
CREATE INDEX `idx_agent_goal_attempt_goal` ON `agent_goal_attempt` (`goal_id`, `attempt_no` DESC);
-- Create index "idx_agent_goal_attempt_active" to table: "agent_goal_attempt"
CREATE INDEX `idx_agent_goal_attempt_active` ON `agent_goal_attempt` (`status`) WHERE status IN ('queued','running');
-- Create index "idx_agent_goal_attempt_lease" to table: "agent_goal_attempt"
CREATE INDEX `idx_agent_goal_attempt_lease` ON `agent_goal_attempt` (`lease_expires_at`) WHERE status IN ('queued','running');
-- Create index "uniq_agent_goal_active_attempt" to table: "agent_goal_attempt"
CREATE UNIQUE INDEX `uniq_agent_goal_active_attempt` ON `agent_goal_attempt` (`goal_id`, `purpose`) WHERE status IN ('queued','running');
-- Create index "uniq_agent_goal_attempt_no" to table: "agent_goal_attempt"
CREATE UNIQUE INDEX `uniq_agent_goal_attempt_no` ON `agent_goal_attempt` (`goal_id`, `purpose`, `attempt_no`);
-- Create "agent_goal_edge" table
CREATE TABLE `agent_goal_edge` (
  `goal_id` text NOT NULL,
  `upstream_id` text NOT NULL,
  `edge_kind` text NOT NULL DEFAULT 'hard',
  `on_failure` text NOT NULL DEFAULT 'block',
  `waived_at` text NULL,
  `waived_by_user` text NULL,
  `waiver_reason` text NOT NULL DEFAULT '',
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`goal_id`, `upstream_id`),
  CONSTRAINT `0` FOREIGN KEY (`waived_by_user`) REFERENCES `auth_user` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `1` FOREIGN KEY (`upstream_id`) REFERENCES `agent_goal` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `2` FOREIGN KEY (`goal_id`) REFERENCES `agent_goal` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CHECK (goal_id != upstream_id)
);
-- Create index "idx_agent_goal_edge_upstream" to table: "agent_goal_edge"
CREATE INDEX `idx_agent_goal_edge_upstream` ON `agent_goal_edge` (`upstream_id`);
-- Create "agent_goal_acceptance_event" table
CREATE TABLE `agent_goal_acceptance_event` (
  `id` text NOT NULL,
  `goal_id` text NOT NULL,
  `attempt_id` text NULL,
  `seq` integer NOT NULL,
  `item_id` text NOT NULL,
  `item_kind` text NOT NULL,
  `result` text NOT NULL,
  `command` text NOT NULL DEFAULT '',
  `exit_code` integer NULL,
  `cache_key` text NOT NULL DEFAULT '',
  `authority` text NOT NULL DEFAULT 'system',
  `reviewer_user_id` text NULL,
  `reviewer_attempt_id` text NULL,
  `rationale` text NOT NULL DEFAULT '',
  `scope` text NOT NULL DEFAULT '',
  `scope_hash` text NOT NULL DEFAULT '',
  `detail` text NOT NULL DEFAULT '{}',
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`reviewer_attempt_id`) REFERENCES `agent_goal_attempt` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `1` FOREIGN KEY (`reviewer_user_id`) REFERENCES `auth_user` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `2` FOREIGN KEY (`attempt_id`) REFERENCES `agent_goal_attempt` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `3` FOREIGN KEY (`goal_id`) REFERENCES `agent_goal` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CHECK (seq >= 0),
  CHECK ((item_kind = 'deterministic' AND exit_code IS NOT NULL)
           OR (item_kind = 'judgment' AND exit_code IS NULL))
);
-- Create index "idx_agent_goal_accept_evt_goal" to table: "agent_goal_acceptance_event"
CREATE INDEX `idx_agent_goal_accept_evt_goal` ON `agent_goal_acceptance_event` (`goal_id`, `seq`);
-- Create index "idx_agent_goal_accept_evt_attempt" to table: "agent_goal_acceptance_event"
CREATE INDEX `idx_agent_goal_accept_evt_attempt` ON `agent_goal_acceptance_event` (`attempt_id`);
-- Create index "uniq_agent_goal_accept_evt" to table: "agent_goal_acceptance_event"
CREATE UNIQUE INDEX `uniq_agent_goal_accept_evt` ON `agent_goal_acceptance_event` (`goal_id`, `attempt_id`, `item_id`, `cache_key`);
-- Create index "idx_agent_goal_accept_evt_cache" to table: "agent_goal_acceptance_event"
CREATE INDEX `idx_agent_goal_accept_evt_cache` ON `agent_goal_acceptance_event` (`cache_key`, `created_at` DESC) WHERE item_kind = 'deterministic' AND cache_key != '';
-- Create "agent_goal_revision" table
CREATE TABLE `agent_goal_revision` (
  `id` text NOT NULL,
  `goal_id` text NOT NULL,
  `revision_no` integer NOT NULL DEFAULT 1,
  `status` text NOT NULL DEFAULT 'draft',
  `review_policy` text NOT NULL DEFAULT 'none',
  `content` text NOT NULL DEFAULT '{}',
  `source_attempt_id` text NULL,
  `planning_session_id` text NULL,
  `accepted_at` text NULL,
  `materialized_at` text NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`planning_session_id`) REFERENCES `ctx_conversation` (`session_id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `1` FOREIGN KEY (`source_attempt_id`) REFERENCES `agent_goal_attempt` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `2` FOREIGN KEY (`goal_id`) REFERENCES `agent_goal` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CHECK (revision_no >= 1),
  CHECK (materialized_at IS NULL OR accepted_at IS NOT NULL)
);
-- Create index "idx_agent_goal_revision_goal" to table: "agent_goal_revision"
CREATE INDEX `idx_agent_goal_revision_goal` ON `agent_goal_revision` (`goal_id`, `revision_no` DESC);
-- Create index "idx_agent_goal_revision_planning_session" to table: "agent_goal_revision"
CREATE INDEX `idx_agent_goal_revision_planning_session` ON `agent_goal_revision` (`planning_session_id`);
-- Create index "idx_agent_goal_revision_source_attempt" to table: "agent_goal_revision"
CREATE INDEX `idx_agent_goal_revision_source_attempt` ON `agent_goal_revision` (`source_attempt_id`);
-- Create index "uniq_agent_goal_revision_no" to table: "agent_goal_revision"
CREATE UNIQUE INDEX `uniq_agent_goal_revision_no` ON `agent_goal_revision` (`goal_id`, `revision_no`);
-- Create index "uniq_agent_goal_open_revision" to table: "agent_goal_revision"
CREATE UNIQUE INDEX `uniq_agent_goal_open_revision` ON `agent_goal_revision` (`goal_id`) WHERE status IN ('draft','in_review');
-- Create index "uniq_agent_goal_materialized_revision" to table: "agent_goal_revision"
CREATE UNIQUE INDEX `uniq_agent_goal_materialized_revision` ON `agent_goal_revision` (`goal_id`) WHERE materialized_at IS NOT NULL;
-- Enable back the enforcement of foreign-keys constraints
PRAGMA foreign_keys = on;
