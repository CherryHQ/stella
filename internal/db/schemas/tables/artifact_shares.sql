CREATE TABLE artifact_share (
    id TEXT PRIMARY KEY,
    token_hash TEXT NOT NULL UNIQUE,
    user_id TEXT NOT NULL REFERENCES auth_users(id) ON DELETE CASCADE,
    session_id TEXT NOT NULL,
    path TEXT NOT NULL,
    media_type TEXT NOT NULL,
    content BLOB NOT NULL,
    expires_at TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX idx_artifact_share_user ON artifact_share(user_id, created_at DESC);
