-- name: CreateAuthPolicy :one
INSERT INTO auth_policy (id, name, effect, subjects, actions, resources, conditions, priority, is_system, enabled)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetAuthPolicy :one
SELECT * FROM auth_policy WHERE id = ?;

-- name: ListAuthPolicies :many
SELECT * FROM auth_policy ORDER BY priority DESC, name;

-- name: ListEnabledAuthPolicies :many
SELECT * FROM auth_policy WHERE enabled = 1 ORDER BY priority DESC, name;

-- name: UpdateAuthPolicy :exec
UPDATE auth_policy SET
    name = ?,
    effect = ?,
    subjects = ?,
    actions = ?,
    resources = ?,
    conditions = ?,
    priority = ?,
    enabled = ?
WHERE id = ?;

-- name: DeleteAuthPolicy :exec
DELETE FROM auth_policy WHERE id = ?;
