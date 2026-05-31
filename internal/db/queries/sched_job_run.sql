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
