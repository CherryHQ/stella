CREATE TABLE recally_article (
    id            TEXT NOT NULL PRIMARY KEY,
    user_id       TEXT NOT NULL REFERENCES auth_user(id) ON DELETE CASCADE,
    agent_id      TEXT REFERENCES agent(id) ON DELETE SET NULL,
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

CREATE UNIQUE INDEX idx_recally_article_user_canonical ON recally_article (user_id, canonical_url);
CREATE INDEX idx_recally_article_user_status ON recally_article (user_id, status);
CREATE INDEX idx_recally_article_user_source ON recally_article (user_id, source_type);
CREATE INDEX idx_recally_article_user_starred ON recally_article (user_id, starred) WHERE starred = 1;
CREATE INDEX idx_recally_article_saved_at ON recally_article (saved_at);

-- TODO: Add FTS5 virtual table for full-text search in a future phase.
-- FTS5 requires a compiled SQLite extension that may not be available in Atlas.
-- For Phase 1 MVP, we use LIKE-based search on title/summary/tags.
