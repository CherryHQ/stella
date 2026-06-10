-- name: CreateSchedJobRun :one
INSERT INTO sched_job_run (id, job_id, session_id, status, started_at, finished_at, error, user_id)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: UpdateSchedJobRun :exec
UPDATE sched_job_run
SET status = ?, finished_at = ?, error = ?
WHERE id = ? AND job_id = ?;

-- name: ListSchedJobRuns :many
SELECT * FROM sched_job_run
WHERE job_id = sqlc.arg('job_id')
  AND (sqlc.narg('user_id') IS NULL OR user_id = sqlc.narg('user_id'))
ORDER BY started_at DESC, id DESC
LIMIT sqlc.arg('limit') OFFSET sqlc.arg('offset');

-- name: GetSchedJobRun :one
SELECT * FROM sched_job_run WHERE id = ? AND job_id = ?;

-- name: CountRunningSchedJobRuns :one
SELECT COUNT(*) FROM sched_job_run
WHERE job_id = ? AND status = 'running';

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
  AND datetime(r.finished_at) >= datetime(sqlc.arg(since))
  AND (sqlc.narg(agent_id) IS NULL OR j.agent_id = sqlc.narg(agent_id))
ORDER BY r.finished_at DESC, r.id DESC
LIMIT sqlc.arg(limit_count);
