-- name: CreateOAuthClient :one
INSERT INTO oauth_client (
    client_id, name, client_secret_hash, client_type, redirect_uris, grant_types, scopes, owner_user_id
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: GetOAuthClientByClientID :one
SELECT * FROM oauth_client
WHERE client_id = $1;

-- name: ListOAuthClientByOwner :many
SELECT * FROM oauth_client
WHERE owner_user_id = $1
ORDER BY created_at DESC, id DESC;

-- name: UpdateOAuthClientSecret :execrows
UPDATE oauth_client
SET client_secret_hash = $2, updated_at = now()
WHERE client_id = $1 AND owner_user_id = $3;

-- name: DisableOAuthClient :execrows
UPDATE oauth_client
SET disabled_at = now(), updated_at = now()
WHERE client_id = $1 AND owner_user_id = $2 AND disabled_at IS NULL;
