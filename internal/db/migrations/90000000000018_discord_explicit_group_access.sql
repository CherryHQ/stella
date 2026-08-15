-- +goose Up
-- Discord gains an explicit guild/channel/user/role allowlist alongside
-- `allow_group`. A new config now fails closed: with `allow_group` on and no
-- allowlist entries, `allow_all_guilds` defaults to false and nothing guild-side
-- is served. A config that was already relying on `allow_group` alone to reach
-- every joined server would otherwise go dark on upgrade, so backfill the
-- explicit `allow_all_guilds: true` opt-in for it. The key is only ever set
-- when absent, so an operator who has already saved this config after the
-- allowlist fields shipped (even with an explicit `false`) is left untouched.
UPDATE channel
SET config = (config::jsonb || jsonb_build_object('allow_all_guilds', true))::text,
    updated_at = now()
WHERE type = 'discord'
  AND config IS JSON OBJECT
  AND coalesce((config::jsonb ->> 'allow_group')::boolean, false)
  AND NOT (config::jsonb ? 'allow_all_guilds');

-- +goose Down
-- Only the backfilled key is removed. The migration cannot tell a backfilled
-- value apart from one an operator set deliberately afterward, and dropping the
-- guild/channel/user/role ID lists here would be an irreversible loss of
-- operator-entered allowlist data, so those keys are left alone.
UPDATE channel
SET config = (config::jsonb - 'allow_all_guilds')::text,
    updated_at = now()
WHERE type = 'discord'
  AND config IS JSON OBJECT
  AND config::jsonb ? 'allow_all_guilds';
