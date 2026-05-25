-- name: CreateAgent :one
INSERT INTO settings_agents (id, name, model, model_strong, model_fast, system_prompt, soul, workspace, sandbox, enabled_builtin_skills, scope, creator_id, enabled, org_id)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetAgent :one
SELECT * FROM settings_agents WHERE id = ?;

-- name: ListAgents :many
SELECT * FROM settings_agents ORDER BY name;

-- name: ListEnabledAgents :many
SELECT * FROM settings_agents WHERE enabled = 1 ORDER BY name;

-- name: UpdateAgent :exec
UPDATE settings_agents SET
    name = ?,
    model = ?,
    model_strong = ?,
    model_fast = ?,
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
DELETE FROM settings_agents WHERE id = ?;

-- name: ListAgentsByOrg :many
SELECT * FROM settings_agents WHERE org_id = ? ORDER BY name;

-- name: ListEnabledAgentsByOrg :many
SELECT * FROM settings_agents WHERE org_id = ? AND enabled = 1 ORDER BY name;

-- name: SetAgentOrg :exec
UPDATE settings_agents SET org_id = ? WHERE id = ?;
