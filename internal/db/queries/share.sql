-- name: CreateShare :one
INSERT INTO share (
    id, token_hash, user_id, title,
    media_type, content, expires_at
) VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: GetShareByTokenHash :one
SELECT * FROM share
WHERE token_hash = $1
  AND (expires_at IS NULL OR expires_at > now());

-- name: ListSharesByUser :many
SELECT id, token_hash, user_id, title,
       media_type, expires_at, created_at, updated_at
FROM share
WHERE user_id = $1
ORDER BY created_at DESC, id DESC
LIMIT $2 OFFSET $3;

-- name: DeleteShareByUser :execrows
DELETE FROM share
WHERE id = $1 AND user_id = $2;
