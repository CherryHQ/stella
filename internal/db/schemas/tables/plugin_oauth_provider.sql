CREATE TABLE plugin_oauth_provider (
    id                TEXT PRIMARY KEY,
    provider_id       TEXT UNIQUE NOT NULL,
    client_id         TEXT NOT NULL DEFAULT '',
    client_secret_enc TEXT NOT NULL DEFAULT '',
    redirect_url      TEXT NOT NULL DEFAULT '',
    created_at        TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at        TEXT NOT NULL DEFAULT (datetime('now'))
);
