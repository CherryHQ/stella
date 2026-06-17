-- name: CreateAuthUserToken :one
INSERT INTO auth_user_token (
    id,
    user_id,
    name,
    token_hash,
    token_prefix,
    auto_generated,
    expires_at
) VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: GetAuthUserTokenByHash :one
SELECT * FROM auth_user_token
WHERE token_hash = $1;

-- name: GetActiveAuthUserTokenByHash :one
SELECT * FROM auth_user_token
WHERE token_hash = $1
  AND revoked_at IS NULL
  AND (expires_at IS NULL OR expires_at > now());

-- name: GetActiveAutoAuthUserTokenByUser :one
-- expires_at is intentionally not filtered here: TokenService rotates at
-- autoTokenRotateAfter (60 days), which is always before autoTokenTTL (90 days),
-- so an auto token is replaced before it can expire. The Go layer handles
-- time-based rotation rather than relying on the DB expiry column.
SELECT * FROM auth_user_token
WHERE user_id = $1
  AND auto_generated = true
  AND revoked_at IS NULL
ORDER BY created_at DESC, id DESC
LIMIT 1;

-- name: RotateAuthUserToken :execrows
UPDATE auth_user_token
SET revoked_at = now(),
    rotated_at = now(),
    updated_at = now()
WHERE id = $1
  AND revoked_at IS NULL
  AND rotated_at IS NULL;

-- name: RevokeAuthUserToken :execrows
UPDATE auth_user_token
SET revoked_at = now(),
    updated_at = now()
WHERE id = $1
  AND revoked_at IS NULL;

-- name: UpdateAuthUserTokenLastUsed :execrows
UPDATE auth_user_token
SET last_used_at = now(),
    updated_at = now()
WHERE id = $1
  AND (
      last_used_at IS NULL
      OR last_used_at <= now() - interval '5 minutes'
  );
