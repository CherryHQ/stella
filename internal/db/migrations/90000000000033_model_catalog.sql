-- +goose Up
CREATE TABLE model_catalog (
    id TEXT PRIMARY KEY,
    payload JSONB NOT NULL,
    etag TEXT NOT NULL DEFAULT '',
    synced_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE model_catalog;
