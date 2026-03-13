CREATE TABLE scheduler_jobs (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    schedule_cron TEXT NOT NULL DEFAULT '',
    schedule_every TEXT NOT NULL DEFAULT '',
    schedule_at TEXT NOT NULL DEFAULT '',
    message TEXT NOT NULL,
    session_mode TEXT NOT NULL DEFAULT 'reuse',
    enabled INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
