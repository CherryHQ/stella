-- name: ListAgentProviderCredential :many
-- Metadata only: api_key_enc is deliberately not selected so credential listings
-- never carry secret material.
SELECT provider_id, updated_at
FROM agent_provider_credential
WHERE agent_id = $1
ORDER BY provider_id;

-- name: GetAgentProviderCredential :one
SELECT * FROM agent_provider_credential
WHERE agent_id = $1 AND provider_id = $2;

-- name: CreateAgentProviderCredential :one
-- Plain insert (not upsert) so a duplicate (agent_id, provider_id) fails the
-- enclosing transaction; the atomic Agent+credentials create relies on this.
INSERT INTO agent_provider_credential (agent_id, provider_id, api_key_enc)
VALUES ($1, $2, $3)
RETURNING *;

-- name: UpsertAgentProviderCredential :one
-- Rotation writes the new ciphertext atomically; on conflict the existing row's
-- key is replaced in a single statement, so a failed write leaves the old row
-- untouched.
INSERT INTO agent_provider_credential (agent_id, provider_id, api_key_enc)
VALUES ($1, $2, $3)
ON CONFLICT (agent_id, provider_id)
DO UPDATE SET api_key_enc = excluded.api_key_enc, updated_at = now()
RETURNING *;

-- name: DeleteAgentProviderCredential :exec
DELETE FROM agent_provider_credential
WHERE agent_id = $1 AND provider_id = $2;
