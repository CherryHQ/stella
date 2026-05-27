CREATE TABLE recally_rss_feed (
    id              TEXT PRIMARY KEY,
    user_id         TEXT NOT NULL REFERENCES auth_user(id) ON DELETE CASCADE,
    agent_id        TEXT REFERENCES settings_agent(id) ON DELETE SET NULL,
    url             TEXT NOT NULL,
    title           TEXT NOT NULL DEFAULT '',
    description     TEXT NOT NULL DEFAULT '',
    check_interval  TEXT NOT NULL DEFAULT '1h',
    last_checked_at TEXT,
    last_etag       TEXT NOT NULL DEFAULT '',
    last_modified   TEXT NOT NULL DEFAULT '',
    enabled         INTEGER NOT NULL DEFAULT 1,
    created_at      TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at      TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE UNIQUE INDEX idx_rss_feeds_user_url ON recally_rss_feed (user_id, url);

CREATE TABLE rss_feed_entries (
    id         TEXT PRIMARY KEY,
    feed_id    TEXT NOT NULL REFERENCES recally_rss_feed(id) ON DELETE CASCADE,
    guid       TEXT NOT NULL,
    url        TEXT NOT NULL DEFAULT '',
    title      TEXT NOT NULL DEFAULT '',
    status     TEXT NOT NULL DEFAULT 'pending'
               CHECK (status IN ('pending','saved','skipped','error')),
    article_id TEXT REFERENCES recally_article(id) ON DELETE SET NULL,
    attempts   INTEGER NOT NULL DEFAULT 0,
    error_msg  TEXT NOT NULL DEFAULT '',
    discovered_at TEXT NOT NULL DEFAULT (datetime('now')),
    processed_at  TEXT
);

CREATE UNIQUE INDEX idx_rss_entries_feed_guid ON rss_feed_entries (feed_id, guid);
CREATE INDEX idx_rss_entries_status ON rss_feed_entries (status);
