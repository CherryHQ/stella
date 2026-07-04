-- +goose Up
ALTER TABLE vault_entry
    ADD COLUMN inject_always BOOLEAN NOT NULL DEFAULT false;

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

-- +goose Down
DROP TABLE vault_entry_project_binding;
DROP TABLE vault_entry_agent_binding;
ALTER TABLE vault_entry DROP COLUMN inject_always;
