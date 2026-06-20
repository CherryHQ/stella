-- name: CreateAuthPolicy :one
INSERT INTO auth_policy (id, name, effect, subjects, actions, resources, conditions, priority, is_system, enabled)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING *;

-- name: GetAuthPolicy :one
SELECT * FROM auth_policy WHERE id = $1;

-- name: ListAuthPolicies :many
SELECT * FROM auth_policy ORDER BY priority DESC, name;

-- name: ListEnabledAuthPolicies :many
SELECT * FROM auth_policy WHERE enabled = true ORDER BY priority DESC, name;

-- name: UpdateAuthPolicy :exec
UPDATE auth_policy SET
    name = $1,
    effect = $2,
    subjects = $3,
    actions = $4,
    resources = $5,
    conditions = $6,
    priority = $7,
    enabled = $8
WHERE id = $9;

-- name: DeleteAuthPolicy :exec
DELETE FROM auth_policy WHERE id = $1;
