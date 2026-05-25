-- name: CreateAuthPolicy :one
INSERT INTO auth_policies (id, name, effect, subjects, actions, resources, conditions, priority, is_system, enabled, org_id)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetAuthPolicy :one
SELECT * FROM auth_policies WHERE id = ?;

-- name: ListAuthPolicies :many
SELECT * FROM auth_policies ORDER BY priority DESC, name;

-- name: ListEnabledAuthPolicies :many
SELECT * FROM auth_policies WHERE enabled = 1 ORDER BY priority DESC, name;

-- name: UpdateAuthPolicy :exec
UPDATE auth_policies SET
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
DELETE FROM auth_policies WHERE id = ?;
