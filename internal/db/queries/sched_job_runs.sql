-- name: CreateSchedJobRun :one
INSERT INTO sched_job_runs (id, job_id, session_id, status, started_at, finished_at, error)
VALUES (?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: UpdateSchedJobRun :exec
UPDATE sched_job_runs
SET status = ?, finished_at = ?, error = ?
WHERE id = ?;

-- name: ListSchedJobRuns :many
SELECT * FROM sched_job_runs
WHERE job_id = ?
ORDER BY started_at DESC
LIMIT ?;

-- name: GetSchedJobRun :one
SELECT * FROM sched_job_runs WHERE id = ?;

-- name: CountRunningSchedJobRuns :one
SELECT COUNT(*) FROM sched_job_runs
WHERE job_id = ? AND status = 'running';
