-- +goose Up
-- Fold the singleton vision setting, and where possible the embedding model,
-- into one deployment-wide `default_models` setting that also carries the three
-- agent tiers. Agent rows keep their own columns and now mean "override the
-- deployment default", empty meaning "inherit".

INSERT INTO app_setting (key, value)
VALUES ('default_models', '{}')
ON CONFLICT (key) DO NOTHING;

UPDATE app_setting d
SET value = (d.value::jsonb || jsonb_build_object('model_vision', COALESCE(v.value::jsonb ->> 'model', '')))::text,
    updated_at = now()
FROM app_setting v
WHERE d.key = 'default_models' AND v.key = 'vision';

-- The legacy embedding model is a bare model id paired with its own API key, so
-- it can only take the unified "provider/model" form when exactly one provider
-- carries that same key. An unmatched deployment keeps its legacy embedding
-- block verbatim and the lane keeps working until an admin picks a provider.
WITH emb AS (
    SELECT value::jsonb AS j FROM app_setting WHERE key = 'embedding'
), cand AS (
    SELECT p.id FROM provider p, emb
    WHERE COALESCE(emb.j ->> 'api_key', '') <> ''
      AND p.config ->> 'api_key' = emb.j ->> 'api_key'
), picked AS (
    SELECT id FROM cand WHERE (SELECT count(*) FROM cand) = 1
)
UPDATE app_setting d
SET value = (d.value::jsonb || jsonb_build_object('model_embedding', picked.id || '/' || (emb.j ->> 'model')))::text,
    updated_at = now()
FROM picked, emb
WHERE d.key = 'default_models' AND COALESCE(emb.j ->> 'model', '') <> '';

DELETE FROM app_setting WHERE key = 'vision';

-- +goose Down
INSERT INTO app_setting (key, value)
SELECT 'vision', jsonb_build_object('model', COALESCE(d.value::jsonb ->> 'model_vision', ''))::text
FROM app_setting d
WHERE d.key = 'default_models'
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now();

DELETE FROM app_setting WHERE key = 'default_models';
