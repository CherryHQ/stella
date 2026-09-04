-- +goose Up
ALTER TABLE mcp_oauth_flow RENAME TO oauth_flow;
ALTER INDEX idx_mcp_oauth_flow_expires_at RENAME TO idx_oauth_flow_expires_at;

ALTER TABLE oauth_flow
    ADD COLUMN provider_key TEXT,
    ADD COLUMN target_kind TEXT,
    ADD COLUMN target_id TEXT,
    ADD COLUMN bundle_name TEXT,
    ADD COLUMN flow_type TEXT NOT NULL DEFAULT 'authorization_code',
    ADD COLUMN verification_uri TEXT NOT NULL DEFAULT '',
    ADD COLUMN user_code TEXT NOT NULL DEFAULT '',
    ADD COLUMN state TEXT NOT NULL DEFAULT 'pending',
    ADD COLUMN error TEXT NOT NULL DEFAULT '',
    ADD COLUMN updated_at TIMESTAMPTZ NOT NULL DEFAULT now();

UPDATE oauth_flow
SET provider_key = 'mcp:' || server_id::text,
    target_kind = 'mcp',
    target_id = server_id::text,
    bundle_name = 'MCP_OAUTH_' || upper(replace(server_id::text, '-', '_'));

ALTER TABLE oauth_flow
    ALTER COLUMN provider_key SET NOT NULL,
    ALTER COLUMN target_kind SET NOT NULL,
    ALTER COLUMN target_id SET NOT NULL,
    ALTER COLUMN bundle_name SET NOT NULL,
    ALTER COLUMN server_id DROP NOT NULL;

CREATE INDEX idx_oauth_flow_owner_provider
    ON oauth_flow (user_id, provider_key, created_at DESC);

-- Preserve the MCP e2e database seam while runtime code moves to the shared
-- table. This simple filtered view remains automatically updatable.
CREATE VIEW mcp_oauth_flow AS
SELECT id, server_id, user_id, credential_scope, credential_user_id,
       credential_agent_id, pkce_verifier, oauth_config, expires_at,
       consumed_at, created_at
FROM oauth_flow
WHERE target_kind = 'mcp';

-- +goose Down
DROP VIEW mcp_oauth_flow;
DROP INDEX idx_oauth_flow_owner_provider;

DELETE FROM oauth_flow WHERE target_kind <> 'mcp';

ALTER TABLE oauth_flow
    ALTER COLUMN server_id SET NOT NULL,
    DROP COLUMN provider_key,
    DROP COLUMN target_kind,
    DROP COLUMN target_id,
    DROP COLUMN bundle_name,
    DROP COLUMN flow_type,
    DROP COLUMN verification_uri,
    DROP COLUMN user_code,
    DROP COLUMN state,
    DROP COLUMN error,
    DROP COLUMN updated_at;

ALTER TABLE oauth_flow RENAME TO mcp_oauth_flow;
ALTER INDEX idx_oauth_flow_expires_at RENAME TO idx_mcp_oauth_flow_expires_at;
