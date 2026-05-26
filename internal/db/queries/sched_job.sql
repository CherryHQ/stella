-- name: CreateSchedulerJob :one
INSERT INTO sched_job (
    id, owner_kind, exec_scope, plugin_id, job_key, runtime_name,
    name, description, schedule_cron, schedule_every, schedule_at,
    message, payload, session_mode, enabled, agent_id, user_id,
    org_id, created_at, updated_at, last_run_at, last_error
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: ListSchedulerJobs :many
SELECT * FROM sched_job WHERE org_id = ? ORDER BY created_at;

-- name: ListSchedulerJobsByAgent :many
SELECT * FROM sched_job
WHERE org_id = ?
  AND (owner_kind IN ('plugin', 'system')
       OR (agent_id = ? AND user_id = ?))
ORDER BY created_at;

-- name: GetSchedulerJob :one
SELECT * FROM sched_job WHERE id = ? AND org_id = ?;

-- name: UpdateSchedulerJob :exec
UPDATE sched_job
SET owner_kind = ?, exec_scope = ?, plugin_id = ?, job_key = ?, runtime_name = ?,
    name = ?, description = ?, schedule_cron = ?, schedule_every = ?, schedule_at = ?,
    message = ?, payload = ?, session_mode = ?, enabled = ?, agent_id = ?, user_id = ?,
    updated_at = ?, last_run_at = ?, last_error = ?
WHERE id = ? AND org_id = ?;

-- name: RecordSchedulerJobRun :exec
UPDATE sched_job
SET last_run_at = ?, last_error = ?, updated_at = ?
WHERE id = ? AND org_id = ?;

-- name: DeleteSchedulerJob :exec
DELETE FROM sched_job WHERE id = ? AND org_id = ?;
