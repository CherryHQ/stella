-- name: CreateAuthRole :one
INSERT INTO auth_roles (id, name, description, is_system)
VALUES (?, ?, ?, ?)
RETURNING *;

-- name: GetAuthRole :one
SELECT * FROM auth_roles WHERE id = ?;

-- name: ListAuthRoles :many
SELECT * FROM auth_roles ORDER BY name;

-- name: UpdateAuthRole :exec
UPDATE auth_roles SET
    name = ?,
    description = ?
WHERE id = ?;

-- name: DeleteAuthRole :exec
DELETE FROM auth_roles WHERE id = ?;
