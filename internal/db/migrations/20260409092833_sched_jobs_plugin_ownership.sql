-- Disable the enforcement of foreign-keys constraints
PRAGMA foreign_keys = off;
-- Create "new_sched_jobs" table
CREATE TABLE `new_sched_jobs` (
  `id` text NULL,
  `owner_kind` text NOT NULL DEFAULT 'user',
  `plugin_id` text NOT NULL DEFAULT '',
  `job_key` text NOT NULL DEFAULT '',
  `runtime_name` text NOT NULL DEFAULT '',
  `name` text NOT NULL,
  `description` text NOT NULL DEFAULT '',
  `schedule_cron` text NOT NULL DEFAULT '',
  `schedule_every` text NOT NULL DEFAULT '',
  `schedule_at` text NOT NULL DEFAULT '',
  `message` text NOT NULL DEFAULT '',
  `payload` text NOT NULL DEFAULT '{}',
  `session_mode` text NOT NULL DEFAULT 'reuse',
  `enabled` integer NOT NULL DEFAULT 1,
  `agent_id` text NULL,
  `user_id` integer NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  `last_run_at` text NULL,
  `last_error` text NOT NULL DEFAULT '',
  PRIMARY KEY (`id`)
);
-- Copy rows from old table "sched_jobs" to new temporary table "new_sched_jobs"
INSERT INTO `new_sched_jobs` (`id`, `name`, `schedule_cron`, `schedule_every`, `schedule_at`, `message`, `session_mode`, `enabled`, `agent_id`, `user_id`, `created_at`) SELECT `id`, `name`, `schedule_cron`, `schedule_every`, `schedule_at`, IFNULL(`message`, '') AS `message`, `session_mode`, `enabled`, `agent_id`, `user_id`, `created_at` FROM `sched_jobs`;
-- Drop "sched_jobs" table after copying rows
DROP TABLE `sched_jobs`;
-- Rename temporary table "new_sched_jobs" to "sched_jobs"
ALTER TABLE `new_sched_jobs` RENAME TO `sched_jobs`;
-- Create index "idx_sched_jobs_owner" to table: "sched_jobs"
CREATE INDEX `idx_sched_jobs_owner` ON `sched_jobs` (`owner_kind`, `plugin_id`, `job_key`);
-- Enable back the enforcement of foreign-keys constraints
PRAGMA foreign_keys = on;
