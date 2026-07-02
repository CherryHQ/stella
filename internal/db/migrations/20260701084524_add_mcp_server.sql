-- +goose Up
-- +goose StatementBegin
-- mcp_server registers an external Model Context Protocol server an agent can
-- connect to over HTTP/SSE. Scope mirrors the skill/vault_entry 4-value model
-- (system / system_agent / user / user_agent) so a registration can be
-- system-wide, per-agent, or per-user. Any auth credential is stored encrypted
-- in vault_entry (see credential_ref); this table holds no secret material.
CREATE TABLE "mcp_server" (
    "id"             UUID PRIMARY KEY DEFAULT uuidv7(),
    "scope"          TEXT NOT NULL,
    "user_id"        UUID REFERENCES "auth_user" ("id") ON DELETE CASCADE,
    "agent_id"       TEXT REFERENCES "agent" ("id") ON DELETE CASCADE,
    "name"           TEXT NOT NULL,
    "url"            TEXT NOT NULL,
    -- Transport is HTTP-based only. Valid values (streamable_http, sse) are
    -- enforced in Go at the write boundary; stdio is rejected so the
    -- multi-tenant sandbox never spawns local processes.
    "transport"      TEXT NOT NULL DEFAULT 'streamable_http',
    -- auth_type valid values (none, bearer) enforced in Go.
    "auth_type"      TEXT NOT NULL DEFAULT 'none',
    -- credential_ref is the vault_entry name holding the encrypted bearer token
    -- for this server (empty when auth_type = none). The secret itself lives in
    -- vault_entry under the same scope, age-encrypted at rest.
    "credential_ref" TEXT NOT NULL DEFAULT '',
    "enabled"        BOOLEAN NOT NULL DEFAULT true,
    "metadata"       JSONB NOT NULL DEFAULT '{}',
    "created_at"     TIMESTAMPTZ NOT NULL DEFAULT now(),
    "updated_at"     TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT "mcp_server_check" CHECK (
        ((scope = 'user') AND (user_id IS NOT NULL) AND (agent_id IS NULL))
        OR ((scope = 'user_agent') AND (user_id IS NOT NULL) AND (agent_id IS NOT NULL))
        OR ((scope = 'system') AND (user_id IS NULL) AND (agent_id IS NULL))
        OR ((scope = 'system_agent') AND (user_id IS NULL) AND (agent_id IS NOT NULL))
    )
);
-- +goose StatementEnd
CREATE UNIQUE INDEX "idx_mcp_server_owner_name" ON "mcp_server" ("name", "scope", (COALESCE((user_id)::text, '')), (COALESCE(agent_id, '')));
CREATE INDEX "idx_mcp_server_visibility" ON "mcp_server" ("scope", "user_id", "agent_id");

-- +goose Down
DROP TABLE "mcp_server";
