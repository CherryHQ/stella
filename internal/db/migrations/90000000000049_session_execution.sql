-- +goose Up
CREATE TABLE ctx_session_execution (
    session_id TEXT PRIMARY KEY REFERENCES ctx_conversation(session_id) ON DELETE CASCADE,
    token UUID NOT NULL DEFAULT uuidv7(),
    lease_until TIMESTAMPTZ NOT NULL,
    cancel_requested BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_ctx_session_execution_lease_until ON ctx_session_execution(lease_until);

-- +goose Down
DROP TABLE ctx_session_execution;
