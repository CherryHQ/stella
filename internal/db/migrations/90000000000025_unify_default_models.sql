-- +goose Up
-- Collapse the separate vision and embedding model settings into one
-- deployment-wide `default_models` setting that also carries the three agent
-- tiers. Agent rows keep their own columns and now mean "override the deployment
-- default", empty meaning "inherit".
--
-- No values are carried over. Both legacy settings named a model in a form the
-- unified one cannot express — vision as a bare model id, embedding as a bare id
-- paired with its own inline API key — so any carry-over would have to guess
-- which provider row they meant. Guessing wrong on embedding is not a cosmetic
-- error: it would file new vectors into an existing space under a different
-- account's model. An admin re-picks both on the models page, which is one
-- explicit choice instead of a silent one.

INSERT INTO app_setting (key, value)
VALUES ('default_models', '{}')
ON CONFLICT (key) DO NOTHING;

DELETE FROM app_setting WHERE key = 'vision';

-- The embedding row survives, minus the model and credentials it used to carry:
-- those now live in `default_models` / the provider catalog. Its own knobs stay.
UPDATE app_setting
SET value = (value::jsonb - 'model' - 'api_key' - 'base_url')::text,
    updated_at = now()
WHERE key = 'embedding';

-- +goose Down
DELETE FROM app_setting WHERE key = 'default_models';
