-- name: CreateAuthUser :one
INSERT INTO auth_users (username, password_hash)
VALUES (?, ?)
RETURNING *;

-- name: GetAuthUser :one
SELECT * FROM auth_users WHERE id = ?;

-- name: GetAuthUserByUsername :one
SELECT * FROM auth_users WHERE username = ?;

-- name: ListAuthUsers :many
SELECT * FROM auth_users ORDER BY username;

-- name: UpdateAuthUser :exec
UPDATE auth_users SET
    username = ?,
    password_hash = ?,
    is_active = ?,
    updated_at = datetime('now')
WHERE id = ?;

-- name: UpdateAuthUserRole :exec
UPDATE auth_users SET
    role = ?,
    updated_at = datetime('now')
WHERE id = ?;

-- name: UpdateAuthUserDefaultAgent :exec
UPDATE auth_users SET
    default_agent_id = ?,
    updated_at = datetime('now')
WHERE id = ?;

-- name: UpdateAuthUserNotifyIdentity :exec
UPDATE auth_users SET
    notify_identity_id = ?,
    updated_at = datetime('now')
WHERE id = ?;

-- name: DeleteAuthUser :exec
DELETE FROM auth_users WHERE id = ?;

-- name: CountAuthUsers :one
SELECT COUNT(*) FROM auth_users;
