-- name: ListProviderModelsCache :many
SELECT * FROM provider_models_cache ORDER BY provider_id;

-- name: UpsertProviderModelsCache :exec
-- Replace the fetched model IDs for one provider; other providers are untouched.
INSERT INTO provider_models_cache (provider_id, models, updated_at)
VALUES ($1, $2, now())
ON CONFLICT (provider_id) DO UPDATE
SET models = EXCLUDED.models,
    updated_at = now();
