-- ClaimWorkflowRun returns claimed=true for a new insert and false for an
-- existing idempotency row. The no-op update keeps this a single round trip
-- while preserving the existing row values for resume logic.
-- name: ClaimWorkflowRun :one
INSERT INTO agent_workflow_run (
    id, workflow_id, workflow_version, idempotency_key, status, inputs, plan_hash
)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (workflow_id, idempotency_key) DO UPDATE
SET id = agent_workflow_run.id
RETURNING agent_workflow_run.*, (xmax = 0) AS claimed;

-- name: GetWorkflowRunByKey :one
SELECT * FROM agent_workflow_run
WHERE workflow_id = $1 AND idempotency_key = $2;

-- name: SetWorkflowRunRoot :exec
UPDATE agent_workflow_run
SET root_goal_id = sqlc.arg(root_goal_id),
    plan_hash = sqlc.arg(plan_hash),
    status = 'materializing',
    updated_at = now()
WHERE id = sqlc.arg(id);

-- name: SetWorkflowRunStatus :exec
UPDATE agent_workflow_run
SET status = sqlc.arg(status),
    updated_at = now()
WHERE id = sqlc.arg(id);

-- name: GetLatestWorkflowRun :one
SELECT * FROM agent_workflow_run
WHERE workflow_id = $1
ORDER BY created_at DESC, id DESC
LIMIT 1;
