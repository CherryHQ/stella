-- +goose Up
ALTER TABLE vault_entry
    ADD COLUMN description TEXT;

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

-- +goose Down
DROP TABLE vault_exec_secret_audit;
ALTER TABLE vault_entry DROP COLUMN description;
