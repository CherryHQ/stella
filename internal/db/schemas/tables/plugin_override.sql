CREATE TABLE plugin_override (
    plugin_id             TEXT NOT NULL PRIMARY KEY,
    enabled               INTEGER,                      -- nullable: NULL=fallback to manifest default
    session_env_vault_key TEXT NOT NULL DEFAULT '',     -- empty=fallback; non-empty=vault blob holding the session_env override map
    created_at            TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at            TEXT NOT NULL DEFAULT (datetime('now'))
);
