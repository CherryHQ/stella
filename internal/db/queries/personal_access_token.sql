-- name: CreatePersonalAccessToken :one
INSERT INTO personal_access_token (
    public_id,
    user_id,
    name,
    token_hash,
    last4,
    scopes,
    expires_at,
    token_use
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: GetPersonalAccessTokenByPublicID :one
-- Lookup key is the indexed public_id. Revoked/expired filtering is done in the
-- resolver so it can emit a precise error; the row is returned regardless of
-- state here.
SELECT * FROM personal_access_token
WHERE public_id = $1;

-- name: ListPersonalAccessTokenByUser :many
SELECT * FROM personal_access_token
WHERE user_id = $1
  AND token_use = 'personal'
ORDER BY created_at DESC, id DESC;

-- name: ListProvisioningTokenByUser :many
SELECT * FROM personal_access_token
WHERE user_id = $1
  AND token_use = 'provisioning'
ORDER BY created_at DESC, id DESC;

-- name: RevokePersonalAccessToken :execrows
UPDATE personal_access_token
SET revoked_at = now(),
    updated_at = now()
WHERE id = $1
  AND user_id = $2
  AND token_use = 'personal'
  AND revoked_at IS NULL;

-- name: RevokeProvisioningToken :execrows
UPDATE personal_access_token
SET revoked_at = now(),
    updated_at = now()
WHERE id = $1
  AND user_id = $2
  AND token_use = 'provisioning'
  AND revoked_at IS NULL;

-- name: RevokePersonalAccessTokenByUser :execrows
-- Cascade-revoke all of a user's PATs (e.g. on account deactivation).
UPDATE personal_access_token
SET revoked_at = now(),
    updated_at = now()
WHERE user_id = $1
  AND revoked_at IS NULL;

-- name: UpdatePersonalAccessTokenLastUsed :execrows
-- Throttled to at most one write per 5 minutes, mirroring auth_user_token, so a
-- hot token does not write the row on every API request.
UPDATE personal_access_token
SET last_used_at = now(),
    updated_at = now()
WHERE id = $1
  AND (
      last_used_at IS NULL
      OR last_used_at <= now() - interval '5 minutes'
  );
