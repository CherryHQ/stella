-- +goose Up
-- The plugin-owned scheduler capability is gone (see the pkg/plugins removal).
-- plugin_id and runtime_name only ever described plugin-owned rows; job_key
-- stays because user subscriptions store their template key there.
-- Purge any stale plugin-owned rows first: nothing can execute them anymore,
-- and normalizeOwnerKind would silently reclassify them as user jobs.
DELETE FROM sched_job WHERE owner_kind = 'plugin';

-- Rebuild the owner index without plugin_id. owner_kind still leads because
-- the list queries filter on it.
DROP INDEX "idx_sched_job_owner";
CREATE INDEX "idx_sched_job_owner" ON "sched_job" ("owner_kind", "job_key");

ALTER TABLE "sched_job" DROP COLUMN "plugin_id";
ALTER TABLE "sched_job" DROP COLUMN "runtime_name";

-- +goose Down
-- Restores the columns with their original defaults. The deleted plugin-owned
-- rows are not recoverable; Down exists for local iteration, not rollback.
ALTER TABLE "sched_job" ADD COLUMN "plugin_id" text NOT NULL DEFAULT '';
ALTER TABLE "sched_job" ADD COLUMN "runtime_name" text NOT NULL DEFAULT '';

DROP INDEX "idx_sched_job_owner";
CREATE INDEX "idx_sched_job_owner" ON "sched_job" ("owner_kind", "plugin_id", "job_key");
