-- name: CreateSchedulerJob :one
INSERT INTO sched_job (
    id, owner_kind, exec_scope, plugin_id, job_key, runtime_name,
    name, description, schedule_cron, schedule_every, schedule_at,
    message, payload, dispatch_kind, session_mode, enabled, agent_id, user_id,
    created_at, updated_at, last_run_at, last_error
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22)
RETURNING *;

-- name: ListSchedulerJobs :many
SELECT * FROM sched_job ORDER BY created_at;

-- name: ListAllSchedulerJobs :many
SELECT * FROM sched_job ORDER BY created_at;

-- name: ListSchedulerJobsByAgent :many
SELECT * FROM sched_job
WHERE owner_kind IN ('plugin', 'system')
      OR (agent_id = $1 AND user_id = $2)
ORDER BY created_at;

-- name: GetSchedulerJob :one
SELECT * FROM sched_job WHERE id = $1;

-- name: UpdateSchedulerJob :exec
UPDATE sched_job
SET owner_kind = $1, exec_scope = $2, plugin_id = $3, job_key = $4, runtime_name = $5,
    name = $6, description = $7, schedule_cron = $8, schedule_every = $9, schedule_at = $10,
    message = $11, payload = $12, dispatch_kind = $13, session_mode = $14, enabled = $15, agent_id = $16, user_id = $17,
    updated_at = $18, last_run_at = $19, last_error = $20
WHERE id = $21;

-- name: RecordSchedulerJobRun :exec
UPDATE sched_job
SET last_run_at = $1, last_error = $2, updated_at = $3
WHERE id = $4;

-- name: CountEnabledSchedulerWorkflowJobs :one
SELECT CAST(COUNT(*) AS BIGINT) FROM sched_job
WHERE enabled = true
  AND dispatch_kind = 'workflow'
  AND payload->>'workflow_id' = sqlc.arg(workflow_id)::text;

-- name: DeleteSchedulerJob :exec
DELETE FROM sched_job WHERE id = $1;
