-- +goose Up
-- mcp_oauth_flow is the durable state of one in-flight Authorization Code + PKCE
-- authorization for an MCP registration. It binds the flow to the initiating
-- user (the callback is unauthenticated and re-identifies via the flow row),
-- records where the resulting token bundle will be written (registration scope
-- for shared credential_mode, the initiating user's user scope for per_user),
-- and carries the client + endpoint configuration captured during discovery.
-- The PKCE verifier is stored server-side because the callback arrives at
-- Stella, not at a browser-held client. Flows are one-shot (consumed_at) and
-- short-lived (expires_at); a janitor delete keeps the table small.
CREATE TABLE "mcp_oauth_flow" (
    "id"                 UUID PRIMARY KEY DEFAULT uuidv7(),
    "server_id"          UUID NOT NULL REFERENCES "mcp_server" ("id") ON DELETE CASCADE,
    "user_id"            UUID NOT NULL REFERENCES "auth_user" ("id") ON DELETE CASCADE,
    -- Where the token bundle will be written: the registration's own scope
    -- tuple for shared mode, ('user', user_id, '') for per_user mode.
    "credential_scope"   TEXT NOT NULL,
    "credential_user_id" UUID,
    "credential_agent_id" TEXT,
    "pkce_verifier"      TEXT NOT NULL,
    -- Client and endpoint configuration resolved during StartOAuth:
    -- {client_id, client_secret_ref, token_endpoint, auth_style, resource,
    --  scopes, redirect_uri}. The client secret itself never lands here —
    -- client_secret_ref names the vault entry holding it.
    "oauth_config"       JSONB NOT NULL,
    "expires_at"         TIMESTAMPTZ NOT NULL,
    "consumed_at"        TIMESTAMPTZ,
    "created_at"         TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX "idx_mcp_oauth_flow_expires_at" ON "mcp_oauth_flow" ("expires_at");

-- +goose Down
DROP TABLE "mcp_oauth_flow";
