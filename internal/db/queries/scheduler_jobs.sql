-- name: CreateSchedulerJob :one
INSERT INTO scheduler_jobs (id, name, schedule_cron, schedule_every, schedule_at, message, session_mode, enabled, agent_id, user_id, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: ListSchedulerJobs :many
SELECT * FROM scheduler_jobs ORDER BY created_at;

-- name: GetSchedulerJob :one
SELECT * FROM scheduler_jobs WHERE id = ?;

-- name: DeleteSchedulerJob :exec
DELETE FROM scheduler_jobs WHERE id = ?;
