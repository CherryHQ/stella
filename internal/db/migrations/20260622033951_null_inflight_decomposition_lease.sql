-- +goose Up
-- The reaper (ListStaleAttempts) no longer special-cases purpose='decomposition';
-- it now relies on lease_expires_at being NULL to skip interactive decomposition
-- attempts (which are not enqueued/heartbeated). Older interactive decomposition
-- attempts were minted with lease_expires_at = now() (a non-liveness marker), so
-- without this fix the reaper would bounce their active composites back to draft.
-- NULL the lease on any in-flight decomposition attempt so the new guard holds.
-- Autonomous decomposition attempts minted after this point carry a real lease.
UPDATE agent_goal_attempt
SET lease_expires_at = NULL
WHERE purpose = 'decomposition'
  AND status IN ('queued', 'running');

-- +goose Down
-- One-time data correction; the prior per-attempt lease timestamps are not
-- recoverable and were never a liveness signal. No-op.
SELECT 1;
