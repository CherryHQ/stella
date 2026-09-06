-- +goose Up
-- Remote MCP observations are separate from authored plugin configuration.
-- Legacy mcp_server rows remain untouched until the runtime cutover.
CREATE TABLE mcp_connection_state (
    id UUID PRIMARY KEY DEFAULT uuidv7(),
    config_id UUID NOT NULL REFERENCES plugin_config(id) ON DELETE CASCADE,
    credential_user_id UUID REFERENCES auth_user(id) ON DELETE CASCADE,
    tools JSONB NOT NULL DEFAULT '[]'::jsonb,
    status TEXT NOT NULL DEFAULT 'unknown',
    status_error TEXT NOT NULL DEFAULT '',
    probed_at TIMESTAMPTZ,
    config_revision BIGINT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT mcp_connection_state_tools_array_check CHECK (jsonb_typeof(tools) = 'array'),
    CONSTRAINT mcp_connection_state_revision_check CHECK (config_revision > 0),
    CONSTRAINT mcp_connection_state_identity_key UNIQUE NULLS NOT DISTINCT (config_id, credential_user_id)
);

CREATE INDEX idx_mcp_connection_state_credential_user_id
    ON mcp_connection_state (credential_user_id)
    WHERE credential_user_id IS NOT NULL;

-- +goose Down
DROP TABLE mcp_connection_state;
