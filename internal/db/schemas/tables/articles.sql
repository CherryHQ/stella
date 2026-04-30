CREATE TABLE articles (
    id            TEXT NOT NULL PRIMARY KEY,
    user_id       INTEGER NOT NULL REFERENCES auth_users(id) ON DELETE CASCADE,
    agent_id      TEXT REFERENCES settings_agents(id) ON DELETE SET NULL,
    url           TEXT NOT NULL,
    canonical_url TEXT NOT NULL,
    source_type   TEXT NOT NULL DEFAULT 'web'
                  CHECK (source_type IN ('web','twitter','youtube','github','rss','pdf')),
    title         TEXT NOT NULL DEFAULT '',
    author        TEXT NOT NULL DEFAULT '',
    summary       TEXT NOT NULL DEFAULT '',
    tags          TEXT NOT NULL DEFAULT '[]',
    status        TEXT NOT NULL DEFAULT 'unread'
                  CHECK (status IN ('unread','read','archived')),
    starred       INTEGER NOT NULL DEFAULT 0,
    file_path     TEXT NOT NULL DEFAULT '',
    metadata      TEXT NOT NULL DEFAULT '{}',
    published_at  TEXT,
    saved_at      TEXT NOT NULL DEFAULT (datetime('now')),
    read_at       TEXT,
    created_at    TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at    TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE UNIQUE INDEX idx_articles_user_canonical ON articles (user_id, canonical_url);
CREATE INDEX idx_articles_user_status ON articles (user_id, status);
CREATE INDEX idx_articles_user_source ON articles (user_id, source_type);
CREATE INDEX idx_articles_user_starred ON articles (user_id, starred) WHERE starred = 1;
CREATE INDEX idx_articles_saved_at ON articles (saved_at);

-- TODO: Add FTS5 virtual table for full-text search in a future phase.
-- FTS5 requires a compiled SQLite extension that may not be available in Atlas.
-- For Phase 1 MVP, we use LIKE-based search on title/summary/tags.
