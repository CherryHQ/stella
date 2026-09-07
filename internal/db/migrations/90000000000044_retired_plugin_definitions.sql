-- +goose Up
ALTER TABLE plugin_definition
    ADD COLUMN retired_at TIMESTAMPTZ;

-- +goose Down
ALTER TABLE plugin_definition
    DROP COLUMN retired_at;
