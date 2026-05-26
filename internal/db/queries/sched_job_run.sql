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
WHERE job_id = ?
ORDER BY started_at DESC
LIMIT ?;

-- name: GetSchedJobRun :one
SELECT * FROM sched_job_run WHERE id = ? AND job_id = ?;

-- name: CountRunningSchedJobRuns :one
SELECT COUNT(*) FROM sched_job_run
WHERE job_id = ? AND status = 'running';
