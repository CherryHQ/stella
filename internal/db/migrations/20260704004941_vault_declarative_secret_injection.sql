-- +goose Up
ALTER TABLE vault_entry
    ADD COLUMN inject_always BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN description TEXT;

CREATE TABLE vault_entry_agent_binding (
    vault_entry_id UUID NOT NULL REFERENCES vault_entry(id) ON DELETE CASCADE,
    agent_id       TEXT NOT NULL REFERENCES agent(id) ON DELETE CASCADE,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (vault_entry_id, agent_id)
);
CREATE INDEX idx_vault_entry_agent_binding_agent_id ON vault_entry_agent_binding (agent_id);

CREATE TABLE vault_entry_project_binding (
    vault_entry_id UUID NOT NULL REFERENCES vault_entry(id) ON DELETE CASCADE,
    project_id     UUID NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (vault_entry_id, project_id)
);
CREATE INDEX idx_vault_entry_project_binding_project_id ON vault_entry_project_binding (project_id);

CREATE TABLE vault_exec_secret_audit (
    id           UUID PRIMARY KEY DEFAULT uuidv7(),
    user_id      UUID NOT NULL REFERENCES auth_user(id) ON DELETE CASCADE,
    agent_id     TEXT NOT NULL REFERENCES agent(id) ON DELETE CASCADE,
    session_id   TEXT NOT NULL,
    name         TEXT NOT NULL,
    command_text TEXT NOT NULL CHECK (length(command_text) <= 200),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_vault_exec_secret_audit_user_created ON vault_exec_secret_audit (user_id, created_at DESC);

-- The legacy 90-day auto token is retired; scoped session tokens are the only
-- form and are never stored in the vault.
DELETE FROM vault_entry
WHERE name = 'STELLA_TOKEN';

-- +goose Down
-- The STELLA_TOKEN delete is irreversible: secret plaintext cannot be reconstructed.
DROP TABLE vault_exec_secret_audit;
DROP TABLE vault_entry_project_binding;
DROP TABLE vault_entry_agent_binding;
ALTER TABLE vault_entry
    DROP COLUMN description,
    DROP COLUMN inject_always;
