-- name: CreateOAuthAccessToken :one
INSERT INTO oauth_access_token (
    public_id, token_hash, last4, client_id, user_id, scopes, refresh_family_id, expires_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: GetOAuthAccessTokenByPublicID :one
-- Joins the family so resolution fails closed on a revoked family without a
-- per-row revoked flag: an access token is valid only while its family lives.
SELECT at.*, f.revoked_at AS family_revoked_at
FROM oauth_access_token at
JOIN oauth_refresh_family f ON f.id = at.refresh_family_id
WHERE at.public_id = $1;

-- name: UpdateOAuthAccessTokenLastUsed :execrows
UPDATE oauth_access_token
SET last_used_at = now()
WHERE id = $1
  AND (last_used_at IS NULL OR last_used_at <= now() - interval '5 minutes');
