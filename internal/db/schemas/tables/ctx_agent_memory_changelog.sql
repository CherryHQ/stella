CREATE TABLE ctx_agent_memory_changelog (
    id          UUID PRIMARY KEY DEFAULT uuidv7(),
    user_id     UUID NOT NULL REFERENCES auth_user(id) ON DELETE CASCADE,
    agent_id    TEXT NOT NULL REFERENCES agent(id) ON DELETE CASCADE,
    session_id  TEXT,
    entity_id   TEXT,
    scope       TEXT NOT NULL,
    action      TEXT NOT NULL,
    source      TEXT NOT NULL,
    memory_version_before BIGINT,
    memory_version_after  BIGINT,
    before_text TEXT,
    after_text  TEXT,
    metadata    TEXT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_ctx_agent_memory_changelog_user_agent ON ctx_agent_memory_changelog(user_id, agent_id, scope);
CREATE INDEX idx_ctx_agent_memory_changelog_version ON ctx_agent_memory_changelog(user_id, agent_id, scope, memory_version_after);
CREATE INDEX idx_ctx_agent_memory_changelog_session ON ctx_agent_memory_changelog(session_id);
CREATE INDEX idx_ctx_agent_memory_changelog_created ON ctx_agent_memory_changelog(created_at);
