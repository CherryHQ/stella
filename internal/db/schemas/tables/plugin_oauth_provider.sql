CREATE TABLE plugin_oauth_provider (
    id                TEXT PRIMARY KEY,
    provider_id       TEXT NOT NULL,
    client_id         TEXT NOT NULL DEFAULT '',
    client_secret_enc TEXT NOT NULL DEFAULT '',
    redirect_url      TEXT NOT NULL DEFAULT '',
    org_id            TEXT REFERENCES auth_organization(id) ON DELETE CASCADE,
    created_at        TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at        TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE(provider_id, org_id)
);

CREATE INDEX idx_plugin_oauth_provider_org_id ON plugin_oauth_provider(org_id);
