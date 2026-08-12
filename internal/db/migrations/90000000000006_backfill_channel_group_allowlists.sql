-- +goose Up
-- Preserve access for groups that were already durably enrolled before
-- Telegram and Feishu allowlists became fail-closed. Explicit allowlists,
-- including an intentionally empty one, are never overwritten.
WITH channel_config AS (
  SELECT c.*,
         CASE
           WHEN c.config IS JSON OBJECT THEN c.config::jsonb
           ELSE NULL
         END AS config_json
  FROM channel c
  WHERE c.type IN ('telegram', 'feishu')
), known_chat AS (
  SELECT cgm.reply_channel_id AS channel_id, gs.platform_group_id AS chat_id
  FROM channel_group_member cgm
  JOIN ctx_group_state gs ON gs.id = cgm.group_id
  JOIN channel_config c ON c.id = cgm.reply_channel_id
  WHERE gs.platform = c.type
    AND gs.platform_group_id <> ''

  UNION

  SELECT c.id AS channel_id, jsonb_object_keys(c.config_json -> 'groups') AS chat_id
  FROM channel_config c
  WHERE c.type = 'feishu'
    AND jsonb_typeof(c.config_json -> 'groups') = 'object'
), allowlist AS (
  SELECT channel_id, string_agg(DISTINCT chat_id, ',' ORDER BY chat_id) AS chat_ids
  FROM known_chat
  WHERE chat_id <> ''
  GROUP BY channel_id
)
UPDATE channel c
SET config = (
      source.config_json
      || jsonb_build_object('allowed_chat_ids', allowlist.chat_ids)
    )::text,
    updated_at = now()
FROM allowlist
JOIN channel_config source ON source.id = allowlist.channel_id
WHERE c.id = allowlist.channel_id
  AND source.config_json IS NOT NULL
  AND NOT (source.config_json ? 'allowed_chat_ids');

-- +goose Down
-- The migration cannot distinguish a backfilled value from an operator edit
-- made afterward, so rollback deliberately preserves the effective policy.
SELECT 1;
