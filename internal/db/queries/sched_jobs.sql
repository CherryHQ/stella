-- name: CreateSchedulerJob :one
INSERT INTO sched_jobs (id, name, schedule_cron, schedule_every, schedule_at, message, session_mode, enabled, agent_id, user_id, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: ListSchedulerJobs :many
SELECT * FROM sched_jobs ORDER BY created_at;

-- name: GetSchedulerJob :one
SELECT * FROM sched_jobs WHERE id = ?;

-- name: UpdateSchedulerJob :exec
UPDATE sched_jobs
SET name = ?, schedule_cron = ?, schedule_every = ?, schedule_at = ?,
    message = ?, session_mode = ?, enabled = ?, agent_id = ?, user_id = ?
WHERE id = ?;

-- name: DeleteSchedulerJob :exec
DELETE FROM sched_jobs WHERE id = ?;
