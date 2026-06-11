-- Add column "output" to table: "sched_job_run"
ALTER TABLE `sched_job_run` ADD COLUMN `output` text NOT NULL DEFAULT '';
