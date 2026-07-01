-- name: CreateOAuthAuthorizationCode :one
INSERT INTO oauth_authorization_code (
    code_hash, client_id, user_id, redirect_uri, scopes, code_challenge, code_challenge_method, expires_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: ConsumeOAuthAuthorizationCode :one
-- Single-use exchange: mark consumed only if not already consumed. A second
-- exchange of the same code returns no row, which the caller treats as reuse.
UPDATE oauth_authorization_code
SET consumed_at = now()
WHERE code_hash = $1 AND consumed_at IS NULL
RETURNING *;

-- name: RevokeOAuthAuthorizationCodesForUserClient :execrows
-- Burn any outstanding (unconsumed) codes when a user revokes a grant, so a code
-- issued seconds before the revoke cannot be exchanged into a fresh grant.
UPDATE oauth_authorization_code
SET consumed_at = now()
WHERE user_id = $1 AND client_id = $2 AND consumed_at IS NULL;
