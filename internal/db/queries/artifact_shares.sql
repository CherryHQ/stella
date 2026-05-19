-- name: CreateArtifactShare :one
INSERT INTO artifact_shares (
    id,
    token_hash,
    owner_user_id,
    source_session_id,
    source_path,
    title,
    media_type,
    kind,
    content,
    size_bytes,
    expires_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetArtifactShare :one
SELECT * FROM artifact_shares WHERE id = ?;

-- name: GetArtifactShareByTokenHash :one
SELECT * FROM artifact_shares
WHERE token_hash = ?
  AND revoked_at IS NULL
  AND (expires_at IS NULL OR expires_at > datetime('now'));

-- name: ListArtifactShareByOwner :many
SELECT * FROM artifact_shares
WHERE owner_user_id = ?
ORDER BY created_at DESC, id DESC;

-- name: RevokeArtifactShareByOwner :execrows
UPDATE artifact_shares
SET revoked_at = datetime('now'), updated_at = datetime('now')
WHERE id = ?
  AND owner_user_id = ?
  AND revoked_at IS NULL;

-- name: UpdateArtifactShareLastAccessed :exec
UPDATE artifact_shares
SET last_accessed_at = datetime('now'), updated_at = datetime('now')
WHERE id = ?;
