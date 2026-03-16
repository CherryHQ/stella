-- name: CreateUser :one
INSERT INTO users (external_id, platform, name)
VALUES (?, ?, ?)
RETURNING *;

-- name: GetUser :one
SELECT * FROM users WHERE id = ?;

-- name: GetUserByExternalID :one
SELECT * FROM users WHERE external_id = ? AND platform = ?;

-- name: ListUsers :many
SELECT * FROM users ORDER BY name;

-- name: UpdateUserDefaultAgent :exec
UPDATE users SET
    default_agent_id = ?,
    updated_at = datetime('now')
WHERE id = ?;

-- name: UpsertUser :one
INSERT INTO users (external_id, platform, name)
VALUES (?, ?, ?)
ON CONFLICT(external_id, platform) DO UPDATE SET
    name = CASE WHEN excluded.name != '' THEN excluded.name ELSE users.name END,
    updated_at = datetime('now')
RETURNING *;
