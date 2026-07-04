-- name: CreateSchedJobRun :one
INSERT INTO sched_job_run (id, job_id, session_id, status, started_at, finished_at, error, user_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: UpdateSchedJobRun :exec
UPDATE sched_job_run
SET status = $1, finished_at = $2, error = $3, output = $4
WHERE id = $5 AND job_id = $6;

-- name: SetSchedJobRunRootGoal :exec
UPDATE sched_job_run
SET root_goal_id = $1
WHERE id = $2 AND job_id = $3;

-- name: ListSchedJobRuns :many
SELECT * FROM sched_job_run
WHERE job_id = sqlc.arg('job_id')
  AND (sqlc.narg('user_id')::text IS NULL OR user_id = sqlc.narg('user_id'))
ORDER BY started_at DESC, id DESC
LIMIT sqlc.arg('limit') OFFSET sqlc.arg('offset');

-- name: GetSchedJobRun :one
SELECT * FROM sched_job_run WHERE id = $1 AND job_id = $2;

-- name: CountRunningSchedJobRuns :one
SELECT COUNT(*) FROM sched_job_run
WHERE job_id = $1 AND status = 'running';

-- name: LockSchedJobForRun :exec
-- Transaction-scoped advisory lock keyed on a hash of the job ID. Held until the
-- enclosing transaction ends, it serializes concurrent tryStartJobRun calls for
-- the same job so the running-run check and insert below are atomic under Read
-- Committed without a schema-level unique constraint. hashtextextended maps the
-- text id straight to the 64-bit key pg_advisory_xact_lock expects (matching
-- AdvisoryXactLock); a hash collision only serializes two unrelated jobs
-- occasionally, which is harmless.
SELECT pg_advisory_xact_lock(hashtextextended(sqlc.arg(job_id), 0));

-- name: ListFailedInboxSchedulerRuns :many
SELECT
  r.id AS run_id,
  r.job_id,
  j.agent_id,
  j.name,
  r.error,
  r.finished_at,
  r.started_at
FROM sched_job_run r
JOIN sched_job j ON j.id = r.job_id
WHERE r.user_id = sqlc.arg(user_id)
  AND r.status = 'failed'
  AND r.finished_at >= sqlc.arg(since)
  AND (sqlc.narg(agent_id)::text IS NULL OR j.agent_id = sqlc.narg(agent_id))
ORDER BY r.finished_at DESC, r.id DESC
LIMIT sqlc.arg(limit_count);
