-- name: CreateArtifactShare :one
INSERT INTO artifact_share (
    id, token_hash, user_id, session_id, path,
    media_type, content, expires_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetArtifactShareByTokenHash :one
SELECT * FROM artifact_share
WHERE token_hash = ?
  AND (expires_at IS NULL OR expires_at > datetime('now'));

-- name: ListArtifactShareByUser :many
SELECT id, token_hash, user_id, session_id, path,
       media_type, expires_at, created_at, updated_at
FROM artifact_share
WHERE user_id = ?
ORDER BY created_at DESC, id DESC;

-- name: DeleteArtifactShareByUser :execrows
DELETE FROM artifact_share
WHERE id = ? AND user_id = ?;
