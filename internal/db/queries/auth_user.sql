-- name: ListAuthUsersByIDs :many
SELECT * FROM auth_user WHERE id IN (sqlc.slice('ids')) ORDER BY id;

-- name: UpdateUserRole :exec
UPDATE auth_user SET role = ?, updated_at = datetime('now') WHERE id = ?;

-- name: UpdateUserActive :exec
UPDATE auth_user SET is_active = ?, updated_at = datetime('now') WHERE id = ?;
