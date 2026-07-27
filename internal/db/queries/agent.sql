-- name: CreateAgent :one
INSERT INTO agent (id, name, model, model_thinking, model_strong, model_strong_thinking, model_fast, model_fast_thinking, model_vision, system_prompt, soul, workspace, sandbox, enabled_builtin_skills, scope, creator_id, enabled)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
RETURNING *;

-- name: SeedAgent :exec
INSERT INTO agent (id, name, model, system_prompt, workspace, sandbox, enabled_builtin_skills, scope, enabled)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (id) DO NOTHING;

-- name: GetAgent :one
SELECT * FROM agent WHERE id = $1;

-- name: ListAgents :many
SELECT * FROM agent ORDER BY name;

-- name: ListEnabledAgents :many
SELECT * FROM agent WHERE enabled = true ORDER BY name;

-- name: ListAccessibleAgents :many
SELECT * FROM agent
WHERE enabled = true
  AND (scope = 'system' OR id IN (SELECT agent_id FROM auth_user_agent WHERE user_id = $1))
ORDER BY name;

-- name: UpdateAgent :exec
UPDATE agent SET
    name = $1,
    model = $2,
    model_thinking = $3,
    model_strong = $4,
    model_strong_thinking = $5,
    model_fast = $6,
    model_fast_thinking = $7,
    model_vision = $8,
    system_prompt = $9,
    soul = $10,
    workspace = $11,
    sandbox = $12,
    enabled_builtin_skills = $13,
    scope = $14,
    enabled = $15,
    updated_at = now()
WHERE id = $16;

-- name: DeleteAgent :exec
DELETE FROM agent WHERE id = $1;
