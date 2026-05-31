CREATE TABLE app_setting (
    key        TEXT NOT NULL,
    value      TEXT NOT NULL DEFAULT '{}',
    updated_at TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY(key)
);
