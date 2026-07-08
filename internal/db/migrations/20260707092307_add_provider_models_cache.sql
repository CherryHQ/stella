-- +goose Up
-- Fetched-model cache: the model IDs an admin pulled from each provider's API.
-- One row per provider so a fetch on any replica is visible to all, and the data
-- survives restarts (it used to live in a pod-local $STELLA_HOME/cache/models.json).
-- provider_id references a config-stored provider, not a DB table, so no FK.
CREATE TABLE provider_models_cache (
    provider_id TEXT PRIMARY KEY,
    models      JSONB NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE provider_models_cache;
