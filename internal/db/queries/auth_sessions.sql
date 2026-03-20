-- name: CreateAuthSession :one
INSERT INTO auth_sessions (id, user_id, expires_at)
VALUES (?, ?, ?)
RETURNING *;

-- name: GetAuthSession :one
SELECT * FROM auth_sessions WHERE id = ?;

-- name: DeleteAuthSession :exec
DELETE FROM auth_sessions WHERE id = ?;

-- name: DeleteExpiredAuthSessions :exec
DELETE FROM auth_sessions WHERE expires_at < datetime('now');

-- name: DeleteUserAuthSessions :exec
DELETE FROM auth_sessions WHERE user_id = ?;

-- name: UpdateAuthSessionExpiry :exec
UPDATE auth_sessions SET expires_at = ? WHERE id = ?;
