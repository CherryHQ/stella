CREATE TABLE auth_organization (
    id          TEXT NOT NULL PRIMARY KEY,
    name        TEXT NOT NULL,
    external_id TEXT NOT NULL DEFAULT '',
    source      TEXT NOT NULL DEFAULT 'local',
    created_at  TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at  TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE(source, external_id)
);
