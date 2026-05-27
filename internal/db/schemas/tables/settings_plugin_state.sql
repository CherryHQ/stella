CREATE TABLE settings_plugin_state (
    plugin_id  TEXT NOT NULL,
    scope_kind TEXT NOT NULL,
    scope_id   TEXT NOT NULL DEFAULT '',
    state_key  TEXT NOT NULL,
    value      TEXT NOT NULL DEFAULT '{}',
    org_id     TEXT REFERENCES auth_organization(id) ON DELETE CASCADE,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (plugin_id, scope_kind, scope_id, state_key, org_id)
);

CREATE INDEX idx_settings_plugin_state_org_id ON settings_plugin_state(org_id);
