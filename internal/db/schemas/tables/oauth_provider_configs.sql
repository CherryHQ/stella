CREATE TABLE oauth_provider_configs (
    provider_id   TEXT PRIMARY KEY,
    client_id     TEXT NOT NULL DEFAULT '',
    client_secret TEXT NOT NULL DEFAULT '',
    redirect_url  TEXT NOT NULL DEFAULT '',
    updated_at    TEXT NOT NULL DEFAULT (datetime('now'))
);
