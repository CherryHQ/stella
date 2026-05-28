CREATE TABLE settings_manifest_plugin_override (
    plugin_id             TEXT NOT NULL,
    org_id                TEXT NOT NULL REFERENCES auth_organization(id) ON DELETE CASCADE,
    enabled               INTEGER,                      -- nullable: NULL=fallback to manifest default
    session_env_vault_key TEXT NOT NULL DEFAULT '',     -- empty=fallback; non-empty=vault blob holding the session_env override map
    updated_at            TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY(plugin_id, org_id)
);

CREATE INDEX idx_manifest_plugin_override_org ON settings_manifest_plugin_override(org_id);
