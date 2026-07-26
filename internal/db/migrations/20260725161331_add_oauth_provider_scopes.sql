-- +goose Up
-- Admin-editable OAuth scope override. Empty array means "no override; use the
-- YAML seed default" (D1/D2).
ALTER TABLE plugin_oauth_provider
    ADD COLUMN scopes TEXT[] NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE plugin_oauth_provider
    DROP COLUMN scopes;
