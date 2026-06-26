-- name: CreateWorkflow :one
INSERT INTO agent_workflow (
    owner_kind, user_id, agent_id, name, intent,
    acceptance_contract, convergence_policy, plan, version,
    source_goal_id, workflow_key
)
VALUES (
    sqlc.arg(owner_kind), sqlc.narg(user_id), sqlc.narg(agent_id), sqlc.arg(name), sqlc.arg(intent),
    sqlc.arg(acceptance_contract), sqlc.arg(convergence_policy), sqlc.arg(plan), sqlc.arg(version),
    sqlc.narg(source_goal_id), sqlc.arg(workflow_key)
)
RETURNING *;

-- name: GetWorkflow :one
SELECT * FROM agent_workflow WHERE id = $1;

-- name: ListWorkflowsByUser :many
SELECT * FROM agent_workflow
WHERE user_id = sqlc.arg(user_id)
  AND (sqlc.narg(agent_id)::text IS NULL OR agent_id = sqlc.narg(agent_id)::text)
  AND (sqlc.narg(q)::text IS NULL OR name ILIKE '%' || sqlc.narg(q) || '%' OR intent ILIKE '%' || sqlc.narg(q) || '%')
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg('limit') OFFSET sqlc.arg('offset');

-- name: CountWorkflowsByUser :one
SELECT COUNT(*) FROM agent_workflow
WHERE user_id = sqlc.arg(user_id)
  AND (sqlc.narg(agent_id)::text IS NULL OR agent_id = sqlc.narg(agent_id)::text)
  AND (sqlc.narg(q)::text IS NULL OR name ILIKE '%' || sqlc.narg(q) || '%' OR intent ILIKE '%' || sqlc.narg(q) || '%');

-- name: UpdateWorkflowMeta :one
UPDATE agent_workflow SET
    name = sqlc.arg(name),
    intent = sqlc.arg(intent),
    updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: DeleteWorkflow :execrows
DELETE FROM agent_workflow WHERE id = sqlc.arg(id);
