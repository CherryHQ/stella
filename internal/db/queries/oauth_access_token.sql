-- name: CreateOAuthAccessToken :one
INSERT INTO oauth_access_token (
    public_id, token_hash, last4, client_id, user_id, scopes, refresh_family_id, expires_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: GetOAuthAccessTokenByPublicID :one
SELECT * FROM oauth_access_token
WHERE public_id = $1;

-- name: UpdateOAuthAccessTokenLastUsed :execrows
UPDATE oauth_access_token
SET last_used_at = now()
WHERE id = $1
  AND (last_used_at IS NULL OR last_used_at <= now() - interval '5 minutes');

-- name: RevokeOAuthAccessTokensByFamily :execrows
-- Cascade: revoking a refresh family also kills every access token minted under
-- it (refresh replay, user disconnect, client disable).
UPDATE oauth_access_token
SET revoked_at = now()
WHERE refresh_family_id = $1 AND revoked_at IS NULL;

-- name: RevokeOAuthAccessTokensForUserClient :execrows
UPDATE oauth_access_token
SET revoked_at = now()
WHERE user_id = $1 AND client_id = $2 AND revoked_at IS NULL;
