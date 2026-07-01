-- name: CreateOAuthRefreshFamily :one
-- Open a new revocation family for a grant. Every refresh token in the rotation
-- chain and every access token minted under the grant reference this row.
INSERT INTO oauth_refresh_family (user_id, client_id)
VALUES ($1, $2)
RETURNING *;

-- name: RevokeOAuthRefreshFamily :execrows
-- Kill one family: reuse detection or a single-grant revoke. Access and refresh
-- tokens in the family fail closed at resolve time by joining revoked_at.
UPDATE oauth_refresh_family
SET revoked_at = now()
WHERE id = $1 AND revoked_at IS NULL;

-- name: RevokeOAuthRefreshFamiliesForUserClient :execrows
-- User-initiated per-client revoke: kill every family the user holds for a
-- client. One UPDATE covers all the client's access + refresh tokens.
UPDATE oauth_refresh_family
SET revoked_at = now()
WHERE user_id = $1 AND client_id = $2 AND revoked_at IS NULL;
