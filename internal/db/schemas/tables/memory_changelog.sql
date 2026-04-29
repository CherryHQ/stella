CREATE TABLE memory_changelog (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id     INTEGER NOT NULL,
    agent_id    TEXT NOT NULL,
    session_id  TEXT,
    entity_id   TEXT,
    scope       TEXT NOT NULL CHECK (scope IN ('profile', 'soul', 'constraint', 'skill', 'compaction')),
    action      TEXT NOT NULL CHECK (action IN ('create', 'update', 'delete', 'compact')),
    source      TEXT NOT NULL CHECK (source IN ('user', 'agent', 'reflect', 'system')),
    memory_version_before INTEGER,
    memory_version_after  INTEGER,
    before_text TEXT,
    after_text  TEXT,
    metadata    TEXT,
    created_at  TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX idx_memory_changelog_user_agent ON memory_changelog(user_id, agent_id, scope);
CREATE INDEX idx_memory_changelog_version ON memory_changelog(user_id, agent_id, scope, memory_version_after);
CREATE INDEX idx_memory_changelog_session ON memory_changelog(session_id);
CREATE INDEX idx_memory_changelog_created ON memory_changelog(created_at);
