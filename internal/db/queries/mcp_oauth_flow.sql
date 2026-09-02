-- name: CreateMCPOAuthFlow :one
INSERT INTO mcp_oauth_flow (id, server_id, user_id, credential_scope, credential_user_id, credential_agent_id, pkce_verifier, oauth_config, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING *;

-- name: GetMCPOAuthFlow :one
SELECT * FROM mcp_oauth_flow WHERE id = $1;

-- ConsumeMCPOAuthFlow flips consumed_at in the same statement that reads the
-- row, so a replayed callback cannot ever complete the same flow twice.
-- name: ConsumeMCPOAuthFlow :one
UPDATE mcp_oauth_flow
SET consumed_at = now()
WHERE id = $1 AND consumed_at IS NULL AND expires_at > now()
RETURNING *;

-- name: DeleteExpiredMCPOAuthFlows :execrows
DELETE FROM mcp_oauth_flow WHERE expires_at < now();
