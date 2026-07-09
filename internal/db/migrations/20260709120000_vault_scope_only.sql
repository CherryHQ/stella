-- +goose Up
-- Vault collapses to one concept (scope): delivery is automatic and ambient, so
-- the per-entry injection targeting (bindings), the on-demand declarable audit
-- table, and the inject_always flag are removed. One-way: binding and audit rows
-- are intentionally unrecoverable.
DROP TABLE IF EXISTS vault_exec_secret_audit;
DROP TABLE IF EXISTS vault_entry_project_binding;
DROP TABLE IF EXISTS vault_entry_agent_binding;

ALTER TABLE vault_entry
    DROP COLUMN IF EXISTS inject_always;

-- +goose Down
-- Recreates the removed structures empty; dropped binding/audit rows are gone.
ALTER TABLE vault_entry
    ADD COLUMN IF NOT EXISTS inject_always BOOLEAN NOT NULL DEFAULT false;

CREATE TABLE IF NOT EXISTS vault_entry_agent_binding (
    vault_entry_id UUID NOT NULL REFERENCES vault_entry(id) ON DELETE CASCADE,
    agent_id       TEXT NOT NULL REFERENCES agent(id) ON DELETE CASCADE,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (vault_entry_id, agent_id)
);
CREATE INDEX IF NOT EXISTS idx_vault_entry_agent_binding_agent_id ON vault_entry_agent_binding (agent_id);

CREATE TABLE IF NOT EXISTS vault_entry_project_binding (
    vault_entry_id UUID NOT NULL REFERENCES vault_entry(id) ON DELETE CASCADE,
    project_id     UUID NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (vault_entry_id, project_id)
);
CREATE INDEX IF NOT EXISTS idx_vault_entry_project_binding_project_id ON vault_entry_project_binding (project_id);

CREATE TABLE IF NOT EXISTS vault_exec_secret_audit (
    id           UUID PRIMARY KEY DEFAULT uuidv7(),
    user_id      UUID NOT NULL REFERENCES auth_user(id) ON DELETE CASCADE,
    agent_id     TEXT NOT NULL REFERENCES agent(id) ON DELETE CASCADE,
    session_id   TEXT NOT NULL,
    name         TEXT NOT NULL,
    command_text TEXT NOT NULL CHECK (length(command_text) <= 200),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_vault_exec_secret_audit_user_created ON vault_exec_secret_audit (user_id, created_at DESC);
