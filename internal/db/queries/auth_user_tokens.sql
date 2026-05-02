-- name: CreateAuthUserToken :one
INSERT INTO auth_user_tokens (
    user_id,
    agent_id,
    name,
    token_hash,
    token_prefix,
    auto_generated,
    expires_at
) VALUES (?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetAuthUserTokenByHash :one
SELECT * FROM auth_user_tokens
WHERE token_hash = ?;

-- name: GetActiveAuthUserTokenByHash :one
SELECT * FROM auth_user_tokens
WHERE token_hash = ?
  AND revoked_at IS NULL
  AND (expires_at IS NULL OR expires_at > datetime('now'));

-- name: GetActiveAutoAuthUserTokenByUser :one
-- expires_at is intentionally not filtered here: TokenService rotates at
-- autoTokenRotateAfter (60 days), which is always before autoTokenTTL (90 days),
-- so an auto token is replaced before it can expire. The Go layer handles
-- time-based rotation rather than relying on the DB expiry column.
SELECT * FROM auth_user_tokens
WHERE user_id = ?
  AND auto_generated = 1
  AND revoked_at IS NULL
  AND agent_id IS NULL
ORDER BY created_at DESC, id DESC
LIMIT 1;

-- name: GetActiveAutoAuthAgentTokenByUser :one
SELECT * FROM auth_user_tokens
WHERE user_id = ?
  AND agent_id = ?
  AND auto_generated = 1
  AND revoked_at IS NULL
ORDER BY created_at DESC, id DESC
LIMIT 1;

-- name: RevokeAutoAgentToken :execrows
UPDATE auth_user_tokens
SET revoked_at = datetime('now'),
    updated_at = datetime('now')
WHERE user_id = ?
  AND agent_id = ?
  AND auto_generated = 1
  AND revoked_at IS NULL;

-- name: RotateAuthUserToken :execrows
UPDATE auth_user_tokens
SET revoked_at = datetime('now'),
    rotated_at = datetime('now'),
    updated_at = datetime('now')
WHERE id = ?
  AND revoked_at IS NULL
  AND rotated_at IS NULL;

-- name: RevokeAuthUserToken :execrows
UPDATE auth_user_tokens
SET revoked_at = datetime('now'),
    updated_at = datetime('now')
WHERE id = ?
  AND revoked_at IS NULL;

-- name: UpdateAuthUserTokenLastUsed :execrows
UPDATE auth_user_tokens
SET last_used_at = datetime('now'),
    updated_at = datetime('now')
WHERE id = ?
  AND (
      last_used_at IS NULL
      OR last_used_at <= datetime('now', '-5 minutes')
  );
