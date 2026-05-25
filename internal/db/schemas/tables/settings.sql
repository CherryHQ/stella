CREATE TABLE settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL DEFAULT '{}',
    org_id     TEXT NOT NULL REFERENCES auth_organization(id) ON DELETE CASCADE,
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX idx_settings_org_id ON settings(org_id);
