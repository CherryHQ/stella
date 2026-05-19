CREATE TABLE artifact_shares (
    id TEXT PRIMARY KEY,
    token_hash TEXT NOT NULL UNIQUE,
    owner_user_id TEXT NOT NULL REFERENCES auth_users(id) ON DELETE CASCADE,
    source_session_id TEXT NOT NULL,
    source_path TEXT NOT NULL,
    title TEXT NOT NULL,
    media_type TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('html', 'markdown', 'image', 'pdf')),
    content BLOB NOT NULL,
    size_bytes INTEGER NOT NULL CHECK (size_bytes >= 0),
    expires_at TEXT,
    revoked_at TEXT,
    last_accessed_at TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX idx_artifact_shares_owner_user_id_created_at ON artifact_shares(owner_user_id, created_at DESC);
CREATE INDEX idx_artifact_shares_source_session_id ON artifact_shares(source_session_id);
CREATE INDEX idx_artifact_shares_expires_at ON artifact_shares(expires_at);
