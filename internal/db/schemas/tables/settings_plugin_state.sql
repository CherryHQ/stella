CREATE TABLE settings_plugin_state (
    plugin_id  TEXT NOT NULL,
    scope_kind TEXT NOT NULL,
    scope_id   TEXT NOT NULL DEFAULT '',
    state_key  TEXT NOT NULL,
    value      TEXT NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (plugin_id, scope_kind, scope_id, state_key)
);
