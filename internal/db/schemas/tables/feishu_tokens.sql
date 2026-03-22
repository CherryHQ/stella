CREATE TABLE feishu_tokens (
    open_id            TEXT PRIMARY KEY,
    access_token       TEXT NOT NULL,  -- AES-256-GCM encrypted, base64
    refresh_token      TEXT NOT NULL,  -- AES-256-GCM encrypted, base64
    expires_at         TEXT NOT NULL,  -- ISO 8601
    refresh_expires_at TEXT NOT NULL,  -- ISO 8601
    created_at         TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at         TEXT NOT NULL DEFAULT (datetime('now'))
);
