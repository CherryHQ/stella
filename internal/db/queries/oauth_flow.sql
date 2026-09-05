-- name: CreateOAuthFlow :one
INSERT INTO oauth_flow (
    id, server_id, user_id, credential_scope, credential_user_id,
    credential_agent_id, pkce_verifier, oauth_config, expires_at,
    provider_key, target_kind, target_id, bundle_name, state, error
)
VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9,
    $10, $11, $12, $13, $14, $15
)
RETURNING *;

-- name: GetOAuthFlow :one
SELECT * FROM oauth_flow WHERE id = $1;

-- name: ClaimOAuthFlow :one
UPDATE oauth_flow
SET state = 'completing', consumed_at = now(), updated_at = now()
WHERE id = $1
  AND state = 'pending'
  AND consumed_at IS NULL
  AND expires_at > now()
RETURNING *;

-- name: UpdateOAuthFlow :exec
UPDATE oauth_flow
SET state = $2, error = $3, updated_at = now()
WHERE id = $1;
