-- name: CreateAgent :one
INSERT INTO agents (id, name, provider_id, model, model_strong, model_fast, system_prompt, workspace, enabled)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetAgent :one
SELECT * FROM agents WHERE id = ?;

-- name: ListAgents :many
SELECT * FROM agents ORDER BY name;

-- name: ListEnabledAgents :many
SELECT * FROM agents WHERE enabled = 1 ORDER BY name;

-- name: UpdateAgent :exec
UPDATE agents SET
    name = ?,
    provider_id = ?,
    model = ?,
    model_strong = ?,
    model_fast = ?,
    system_prompt = ?,
    workspace = ?,
    enabled = ?,
    updated_at = datetime('now')
WHERE id = ?;

-- name: DeleteAgent :exec
DELETE FROM agents WHERE id = ?;
