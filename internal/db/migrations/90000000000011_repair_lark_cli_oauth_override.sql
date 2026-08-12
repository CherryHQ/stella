-- +goose Up
-- The #807 cleanup removed managed OAuth fields from tool/lark-cli overrides,
-- but legacy whole-definition rows interpret omitted fields as owned and empty.
-- Convert those cleaned rows to sparse overrides: preserve the legacy snapshot
-- for unrelated editable fields, while letting the restored builtin provider,
-- session env, and managed prompt flow through when #807 removed them.
UPDATE plugin_override
SET config = (
    '{
        "$sparse": true,
        "name": null,
        "display_name": null,
        "description": null,
        "category": null,
        "binaries": null,
        "skills": null
    }'::jsonb
    || (config::jsonb - 'id' - 'kind' - 'enabled' - 'essential' - 'builtin' - 'overridden_fields')
)::text,
updated_at = now()
WHERE plugin_id = 'tool/lark-cli'
  AND btrim(config) <> ''
  AND NOT (config::jsonb ? '$sparse')
  AND NOT (config::jsonb ? 'oauth_provider')
  AND NOT (config::jsonb ? 'session_env');

-- +goose Down
-- Irreversible compatibility repair: the legacy row did not record which
-- omitted fields were intentionally empty, so restoring that ambiguity would
-- break managed OAuth again.
SELECT 1;
