-- name: CreateAgent :one
INSERT INTO agent (id, name, model, model_thinking, model_strong, model_strong_thinking, model_fast, model_fast_thinking, system_prompt, soul, workspace, sandbox, enabled_builtin_skills, scope, creator_id, enabled)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetAgent :one
SELECT * FROM agent WHERE id = ?;

-- name: ListAgents :many
SELECT * FROM agent ORDER BY name;

-- name: ListEnabledAgents :many
SELECT * FROM agent WHERE enabled = 1 ORDER BY name;

-- name: ListAccessibleAgents :many
SELECT * FROM agent
WHERE enabled = 1
  AND (scope = 'system' OR id IN (SELECT agent_id FROM auth_user_agent WHERE user_id = ?))
ORDER BY name;

-- name: UpdateAgent :exec
UPDATE agent SET
    name = ?,
    model = ?,
    model_thinking = ?,
    model_strong = ?,
    model_strong_thinking = ?,
    model_fast = ?,
    model_fast_thinking = ?,
    system_prompt = ?,
    soul = ?,
    workspace = ?,
    sandbox = ?,
    enabled_builtin_skills = ?,
    scope = ?,
    enabled = ?,
    updated_at = datetime('now')
WHERE id = ?;

-- name: DeleteAgent :exec
DELETE FROM agent WHERE id = ?;
