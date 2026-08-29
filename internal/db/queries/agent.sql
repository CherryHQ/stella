-- name: CreateAgent :one
INSERT INTO agent (id, name, model, model_thinking, model_strong, model_strong_thinking, model_fast, model_fast_thinking, system_prompt, soul, workspace, sandbox, scope, creator_id, enabled)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
RETURNING *;

-- name: SeedAgent :exec
INSERT INTO agent (id, name, model, system_prompt, workspace, sandbox, scope, enabled)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (id) DO NOTHING;

-- name: GetAgent :one
SELECT * FROM agent WHERE id = $1;

-- name: GetAgentForUpdate :one
SELECT * FROM agent WHERE id = $1 FOR UPDATE;

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
    system_prompt = $8,
    soul = $9,
    workspace = $10,
    sandbox = $11,
    scope = $12,
    enabled = $13,
    updated_at = now()
WHERE id = $14;

-- name: UpdateAgentIfVersion :one
UPDATE agent SET
    name = $1,
    model = $2,
    model_thinking = $3,
    model_strong = $4,
    model_strong_thinking = $5,
    model_fast = $6,
    model_fast_thinking = $7,
    system_prompt = $8,
    soul = $9,
    workspace = $10,
    sandbox = $11,
    scope = $12,
    enabled = $13,
    updated_at = now()
WHERE id = $14 AND updated_at = $15
RETURNING updated_at;

-- name: GetAgentSkillPolicyForUpdate :one
SELECT enabled_builtin_skills FROM agent WHERE id = $1 FOR UPDATE;

-- name: UpdateAgentSkillPolicy :exec
UPDATE agent
SET enabled_builtin_skills = $1, updated_at = now()
WHERE id = $2;

-- name: DeleteAgent :exec
DELETE FROM agent WHERE id = $1;

-- name: DeleteAgentIfVersion :one
DELETE FROM agent WHERE id = $1 AND updated_at = $2
RETURNING id;
