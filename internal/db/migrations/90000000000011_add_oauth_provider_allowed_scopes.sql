-- +goose Up
-- Empty means "use the provider manifest allowlist". The existing scopes
-- column remains the administrator override for scopes requested by default.
ALTER TABLE plugin_oauth_provider
    ADD COLUMN allowed_scopes TEXT[] NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE plugin_oauth_provider
    DROP COLUMN allowed_scopes;
