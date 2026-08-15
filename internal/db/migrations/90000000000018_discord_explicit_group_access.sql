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
-- No-op by design. The migration cannot tell a backfilled `allow_all_guilds`
-- value apart from one an operator set deliberately afterward (see Up), so
-- removing the key on rollback would be a guess, not a reversal: a config an
-- operator explicitly confirmed as `true` after upgrading would silently lose
-- that explicit policy and, on the next Up, get re-backfilled to the same
-- value anyway. Leaving the explicit key in place keeps every config's
-- guild-access policy exactly what it already evaluates to, on both old and
-- new binaries reading it, which is safer than an ambiguous edit.
SELECT 1;
