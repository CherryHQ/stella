-- name: CreateOAuthRefreshToken :one
INSERT INTO oauth_refresh_token (
    public_id, token_hash, last4, client_id, user_id, scopes, family_id, expires_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: GetOAuthRefreshTokenByPublicID :one
SELECT * FROM oauth_refresh_token
WHERE public_id = $1;

-- name: ConsumeOAuthRefreshToken :one
-- Rotation: mark the presented token consumed and point it at its replacement,
-- only if it is still active. No row returned means it was already consumed
-- (reuse) or revoked, and the caller must revoke the whole family.
UPDATE oauth_refresh_token
SET consumed_at = now(), replaced_by_id = $2
WHERE public_id = $1 AND consumed_at IS NULL AND revoked_at IS NULL
RETURNING *;

-- name: RevokeOAuthRefreshFamily :execrows
UPDATE oauth_refresh_token
SET revoked_at = now()
WHERE family_id = $1 AND revoked_at IS NULL;

-- name: ListOAuthAuthorizedApps :many
-- One row per client the user has an active (non-revoked, unexpired) refresh
-- token for: the user-facing "authorized apps" list. Latest grant per client.
SELECT DISTINCT ON (rt.client_id)
    rt.client_id,
    rt.family_id,
    rt.scopes,
    rt.created_at,
    c.name AS client_name
FROM oauth_refresh_token rt
JOIN oauth_client c ON c.client_id = rt.client_id
WHERE rt.user_id = $1
  AND rt.revoked_at IS NULL
  AND rt.expires_at > now()
ORDER BY rt.client_id, rt.created_at DESC;

-- name: RevokeOAuthGrantForUser :execrows
-- User-initiated per-client revoke: kill every refresh token the user holds for
-- a client. Access-token cascade is handled separately by family.
UPDATE oauth_refresh_token
SET revoked_at = now()
WHERE user_id = $1 AND client_id = $2 AND revoked_at IS NULL;
