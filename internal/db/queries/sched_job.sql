-- name: CreateSchedulerJob :one
INSERT INTO sched_job (
    id, owner_kind, exec_scope, job_key,
    name, description, schedule_cron, schedule_every, schedule_at,
    message, payload, dispatch_kind, session_mode, enabled, agent_id, user_id,
    created_at, updated_at, last_run_at, last_error, idempotency_key
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21)
RETURNING *;

-- name: GetSchedulerJobByIdempotencyKey :one
SELECT * FROM sched_job
WHERE user_id = $1 AND idempotency_key = $2;

-- name: ListSchedulerJobs :many
SELECT * FROM sched_job ORDER BY created_at;

-- name: ListAllSchedulerJobs :many
SELECT * FROM sched_job ORDER BY created_at;

-- name: ListSchedulerJobByOwner :many
SELECT * FROM sched_job
WHERE owner_kind = 'user'
  AND agent_id = $1
  AND user_id = $2
ORDER BY created_at;

-- name: ListSchedulerJobsByAgent :many
SELECT * FROM sched_job
WHERE owner_kind = 'system'
      OR (agent_id = $1 AND user_id = $2)
ORDER BY created_at;

-- name: GetSchedulerJob :one
SELECT * FROM sched_job WHERE id = $1;

-- name: UpdateSchedulerJob :exec
UPDATE sched_job
SET owner_kind = $1, exec_scope = $2, job_key = $3,
    name = $4, description = $5, schedule_cron = $6, schedule_every = $7, schedule_at = $8,
    message = $9, payload = $10, dispatch_kind = $11, session_mode = $12, enabled = $13, agent_id = $14, user_id = $15,
    updated_at = $16, last_run_at = $17, last_error = $18
WHERE id = $19;

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
