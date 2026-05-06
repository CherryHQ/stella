-- Add column "exec_scope" to table: "sched_jobs"
ALTER TABLE `sched_jobs` ADD COLUMN `exec_scope` text NOT NULL DEFAULT 'user';
-- Add column "user_id" to table: "sched_job_runs"
ALTER TABLE `sched_job_runs` ADD COLUMN `user_id` integer NULL;
-- Migrate existing system-scoped rows (no user context) to exec_scope='system' and owner_kind='system'.
-- These are non-plugin rows with no user_id — they were seeded as system builtins.
UPDATE sched_jobs SET exec_scope = 'system', owner_kind = 'system'
    WHERE user_id IS NULL AND owner_kind = 'user';
-- Delete old per-user builtin rows that the new all_users fan-out model replaces with a single row.
DELETE FROM sched_jobs WHERE name = 'recally-rss' AND user_id IS NOT NULL;
