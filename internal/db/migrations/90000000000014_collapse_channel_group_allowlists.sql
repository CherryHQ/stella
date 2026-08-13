-- +goose Up
-- Group access is now a single `allow_group` switch. The per-chat allowlists it
-- replaces (`allowed_chat_ids`, `allowed_conversation_ids`, `allowed_guild_ids`)
-- required IDs that platforms do not surface in their clients, so operators had
-- to mine them from rejection logs.
--
-- A channel that listed at least one ID was already serving groups and keeps
-- serving them, but its reach widens: the switch cannot say "these chats only",
-- so every group the bot belongs to becomes reachable. That widening is the
-- point of the change and is called out in the channel docs; an empty or absent
-- list stays closed, and a list that no Go decoder could ever have read (a
-- non-string value) is treated as closed rather than repaired into access.
UPDATE channel
SET config = (
      (config::jsonb - 'allowed_chat_ids' - 'allowed_conversation_ids' - 'allowed_guild_ids')
      || jsonb_build_object(
           'allow_group',
           (coalesce(jsonb_typeof(config::jsonb -> 'allowed_chat_ids'), '') = 'string'
              AND btrim(config::jsonb ->> 'allowed_chat_ids') <> '')
             OR (coalesce(jsonb_typeof(config::jsonb -> 'allowed_conversation_ids'), '') = 'string'
              AND btrim(config::jsonb ->> 'allowed_conversation_ids') <> '')
             OR (coalesce(jsonb_typeof(config::jsonb -> 'allowed_guild_ids'), '') = 'string'
              AND btrim(config::jsonb ->> 'allowed_guild_ids') <> '')
         )
    )::text,
    updated_at = now()
WHERE type IN ('telegram', 'feishu', 'dingtalk', 'discord')
  AND config IS JSON OBJECT
  AND NOT (config::jsonb ? 'allow_group');

-- +goose Down
-- The individual chat IDs are gone and cannot be reconstructed, so rollback only
-- drops the switch. Older code reads a missing allowlist as deny-all, which is
-- the fail-closed direction; re-list the trusted chats after rolling back.
UPDATE channel
SET config = (config::jsonb - 'allow_group')::text,
    updated_at = now()
WHERE type IN ('telegram', 'feishu', 'dingtalk', 'discord')
  AND config IS JSON OBJECT
  AND config::jsonb ? 'allow_group';
