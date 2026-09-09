-- +goose Up
-- Agent-specific kill switches for trusted, Go-registered native tools. The
-- native ID is deliberately not user-authored or a plugin/config FK: it is
-- validated against the static Go registry before this relation is read or
-- written. Agent ownership remains a real FK so deleting an Agent cannot
-- leave an orphaned deny row.
CREATE TABLE native_agent_deny (
    native_id TEXT NOT NULL,
    agent_id TEXT NOT NULL REFERENCES agent(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (native_id, agent_id)
);

CREATE INDEX idx_native_agent_deny_agent_id ON native_agent_deny (agent_id);

-- +goose Down
DROP TABLE native_agent_deny;
