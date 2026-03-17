CREATE TABLE ctx_conversations (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL UNIQUE,
    title TEXT,
    channel TEXT NOT NULL DEFAULT '',
    archived INTEGER NOT NULL DEFAULT 0,
    last_active TEXT NOT NULL DEFAULT (datetime('now')),
    bootstrapped_at TEXT,
    agent_id TEXT,
    user_id INTEGER,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);
