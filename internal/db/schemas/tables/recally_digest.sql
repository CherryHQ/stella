CREATE TABLE recally_digest (
    id         TEXT NOT NULL PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES auth_user(id) ON DELETE CASCADE,
    date       TEXT NOT NULL,
    narrative  TEXT NOT NULL DEFAULT '',
    saved_yesterday_count  INTEGER NOT NULL DEFAULT 0,
    unread_count           INTEGER NOT NULL DEFAULT 0,
    read_count             INTEGER NOT NULL DEFAULT 0,
    archived_count         INTEGER NOT NULL DEFAULT 0,
    starred_count          INTEGER NOT NULL DEFAULT 0,
    worth_revisiting_count INTEGER NOT NULL DEFAULT 0,
    total_articles         INTEGER NOT NULL DEFAULT 0,
    top_tags   TEXT NOT NULL DEFAULT '[]',
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE UNIQUE INDEX idx_recally_digest_user_date ON recally_digest (user_id, date);
CREATE INDEX idx_recally_digest_user_id ON recally_digest (user_id);

CREATE TABLE recally_digest_article (
    digest_id  TEXT NOT NULL REFERENCES recally_digest(id) ON DELETE CASCADE,
    article_id TEXT NOT NULL REFERENCES recally_article(id) ON DELETE CASCADE,
    section    TEXT NOT NULL,
    position   INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (digest_id, article_id, section)
);

CREATE INDEX idx_recally_digest_article_digest ON recally_digest_article (digest_id);
