-- name: CreateOAuthRefreshToken :one
INSERT INTO oauth_refresh_token (
    public_id, token_hash, client_id, user_id, scopes, family_id, expires_at
) VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: GetOAuthRefreshTokenByPublicID :one
-- Joins the family so the caller sees family-level revocation without a second
-- round-trip: a token whose family is revoked is dead even if its own row looks
-- active.
SELECT rt.*, f.revoked_at AS family_revoked_at
FROM oauth_refresh_token rt
JOIN oauth_refresh_family f ON f.id = rt.family_id
WHERE rt.public_id = $1;

-- name: ConsumeOAuthRefreshToken :one
-- Rotation: mark the presented token consumed and point it at its replacement,
-- only if it is still active. No row returned means it was already consumed
-- (reuse), and the caller must revoke the whole family. Family-level revocation
-- is checked separately at read time via GetOAuthRefreshTokenByPublicID.
UPDATE oauth_refresh_token
SET consumed_at = now(), replaced_by_id = $2
WHERE public_id = $1 AND consumed_at IS NULL
RETURNING *;

-- name: ListOAuthAuthorizedApps :many
-- One row per client the user has an active grant for: the current (unconsumed,
-- unexpired) refresh token of each non-revoked family. The user-facing
-- "authorized apps" list.
SELECT DISTINCT ON (rt.client_id)
    rt.client_id,
    rt.family_id,
    rt.scopes,
    rt.created_at,
    c.name AS client_name
FROM oauth_refresh_token rt
JOIN oauth_client c ON c.client_id = rt.client_id
JOIN oauth_refresh_family f ON f.id = rt.family_id
WHERE rt.user_id = $1
  AND f.revoked_at IS NULL
  AND rt.consumed_at IS NULL
  AND rt.expires_at > now()
ORDER BY rt.client_id, rt.created_at DESC;
