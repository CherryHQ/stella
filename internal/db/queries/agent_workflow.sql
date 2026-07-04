-- name: CreateWorkflow :one
INSERT INTO agent_workflow (
    id, owner_kind, user_id, agent_id, name, version, workflow_key,
    intent, acceptance_contract, convergence_policy, inputs,
    payload_format, payload, fully_frozen, source_goal_id
)
VALUES (
    $1, $2, $3, $4, $5, $6, $7,
    $8, $9, $10, $11,
    $12, $13, $14, $15
)
RETURNING *;

-- name: GetWorkflow :one
SELECT * FROM agent_workflow WHERE id = $1;

-- name: ListWorkflows :many
SELECT * FROM agent_workflow
WHERE user_id IS NOT DISTINCT FROM sqlc.narg(user_id)::uuid
  AND (
    (sqlc.narg(agent_id)::text IS NULL AND owner_kind IN ('user', 'agent'))
    OR (owner_kind = 'agent' AND agent_id IS NOT DISTINCT FROM sqlc.narg(agent_id)::text)
  )
ORDER BY name ASC, version DESC, created_at DESC, id DESC;

-- name: ListWorkflowVersions :many
SELECT * FROM agent_workflow
WHERE owner_kind = sqlc.arg(owner_kind)
  AND user_id IS NOT DISTINCT FROM sqlc.narg(user_id)::uuid
  AND agent_id IS NOT DISTINCT FROM sqlc.narg(agent_id)::text
  AND name = sqlc.arg(name)
ORDER BY version DESC, created_at DESC, id DESC;

-- name: GetLatestWorkflowVersion :one
SELECT COALESCE(MAX(version), 0)::integer FROM agent_workflow
WHERE owner_kind = sqlc.arg(owner_kind)
  AND user_id IS NOT DISTINCT FROM sqlc.narg(user_id)::uuid
  AND agent_id IS NOT DISTINCT FROM sqlc.narg(agent_id)::text
  AND name = sqlc.arg(name);

-- name: DeleteWorkflow :exec
DELETE FROM agent_workflow WHERE id = $1;
