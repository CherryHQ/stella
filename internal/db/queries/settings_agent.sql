-- name: CreateAgent :one
INSERT INTO settings_agent (id, name, model, model_strong, model_fast, system_prompt, soul, workspace, sandbox, enabled_builtin_skills, scope, creator_id, enabled, org_id)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetAgent :one
SELECT * FROM settings_agent WHERE id = ?;

-- name: ListAgents :many
SELECT * FROM settings_agent WHERE org_id = ? ORDER BY name;

-- name: ListEnabledAgents :many
SELECT * FROM settings_agent WHERE org_id = ? AND enabled = 1 ORDER BY name;

-- name: ListAccessibleAgents :many
SELECT * FROM settings_agent
WHERE org_id = ? AND enabled = 1
  AND (scope = 'system' OR id IN (SELECT agent_id FROM auth_user_agent WHERE user_id = ?))
ORDER BY name;

-- name: UpdateAgent :exec
UPDATE settings_agent SET
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
DELETE FROM settings_agent WHERE id = ?;

-- name: SetAgentOrg :exec
UPDATE settings_agent SET org_id = ? WHERE id = ?;
