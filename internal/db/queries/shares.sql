-- name: CreateShare :one
INSERT INTO share (
    id, token_hash, user_id, title,
    media_type, content, expires_at
) VALUES (?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetShareByTokenHash :one
SELECT * FROM share
WHERE token_hash = ?
  AND (expires_at IS NULL OR expires_at > datetime('now'));

-- name: ListSharesByUser :many
SELECT id, token_hash, user_id, title,
       media_type, expires_at, created_at, updated_at
FROM share
WHERE user_id = ?
ORDER BY created_at DESC, id DESC;

-- name: DeleteShareByUser :execrows
DELETE FROM share
WHERE id = ? AND user_id = ?;
