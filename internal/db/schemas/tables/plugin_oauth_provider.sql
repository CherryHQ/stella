CREATE TABLE plugin_oauth_provider (
    id                UUID PRIMARY KEY DEFAULT uuidv7(),
    provider_id       TEXT NOT NULL UNIQUE,
    client_id         TEXT NOT NULL DEFAULT '',
    client_secret_enc TEXT NOT NULL DEFAULT '',
    redirect_url      TEXT NOT NULL DEFAULT '',
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
