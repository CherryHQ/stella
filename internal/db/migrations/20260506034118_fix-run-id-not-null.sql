-- Disable the enforcement of foreign-keys constraints
PRAGMA foreign_keys = off;
-- Create "new_sched_job_runs" table
CREATE TABLE `new_sched_job_runs` (
  `id` text NOT NULL,
  `job_id` text NOT NULL,
  `session_id` text NOT NULL DEFAULT '',
  `status` text NOT NULL DEFAULT 'running',
  `started_at` text NOT NULL DEFAULT (datetime('now')),
  `finished_at` text NULL,
  `error` text NOT NULL DEFAULT '',
  `user_id` integer NULL,
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`job_id`) REFERENCES `sched_jobs` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Copy rows from old table "sched_job_runs" to new temporary table "new_sched_job_runs"
INSERT INTO `new_sched_job_runs` (`id`, `job_id`, `session_id`, `status`, `started_at`, `finished_at`, `error`, `user_id`) SELECT `id`, `job_id`, `session_id`, `status`, `started_at`, `finished_at`, `error`, `user_id` FROM `sched_job_runs`;
-- Drop "sched_job_runs" table after copying rows
DROP TABLE `sched_job_runs`;
-- Rename temporary table "new_sched_job_runs" to "sched_job_runs"
ALTER TABLE `new_sched_job_runs` RENAME TO `sched_job_runs`;
-- Create index "idx_sched_job_runs_job_id" to table: "sched_job_runs"
CREATE INDEX `idx_sched_job_runs_job_id` ON `sched_job_runs` (`job_id`, `started_at` DESC);
-- Enable back the enforcement of foreign-keys constraints
PRAGMA foreign_keys = on;
