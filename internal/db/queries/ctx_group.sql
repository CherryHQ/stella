-- name: GetGroupStateByTriple :one
SELECT * FROM ctx_group_state
WHERE platform = sqlc.arg(platform)
  AND platform_group_id = sqlc.arg(platform_group_id)
  AND platform_thread_id = sqlc.arg(platform_thread_id);

-- name: CreateGroupState :one
INSERT INTO ctx_group_state (id, platform, platform_group_id, platform_thread_id, group_name, created_by_user_id)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: AdoptGroupState :one
-- Renames a group's physical (platform, group, thread) identity in place. Every
-- table that references group_id keeps pointing at this row, so an in-place
-- rename is a lossless identity migration, never a copy.
UPDATE ctx_group_state
SET platform_group_id = sqlc.arg(platform_group_id),
    platform_thread_id = sqlc.arg(platform_thread_id),
    updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: GetGroupStateByID :one
SELECT * FROM ctx_group_state WHERE id = $1;

-- name: GetGroupStateByIDForUpdate :one
SELECT * FROM ctx_group_state WHERE id = $1 FOR UPDATE;

-- name: CountPeerMessagesAfterSeq :one
-- Failed platform deliveries are canonical audit rows, but not peer activity:
-- nobody could have seen them, so later freshness gates must ignore them.
-- System rows are excluded for the same reason in reverse: the stalled-work
-- nudge writes one, and counting it would let recovery invalidate the very turn
-- it is waiting for.
SELECT COUNT(*)::bigint
FROM ctx_group_message
WHERE group_id = sqlc.arg(group_id)
  AND seq > sqlc.arg(after_seq)
  AND NOT (actor_type = 'agent' AND actor_id = sqlc.arg(agent_id))
  AND actor_type != 'system'
  AND delivery_state != 'failed';

-- name: MaxPeerMessageSeqAfterSeq :one
-- Same exclusions as CountPeerMessagesAfterSeq: the two must agree, or a turn is
-- held against a seq the count never saw.
SELECT COALESCE(MAX(seq), 0)::bigint
FROM ctx_group_message
WHERE group_id = sqlc.arg(group_id)
  AND seq > sqlc.arg(after_seq)
  AND NOT (actor_type = 'agent' AND actor_id = sqlc.arg(agent_id))
  AND actor_type != 'system'
  AND delivery_state != 'failed';

-- name: GetLatestPeerGroupMessageWithContent :one
SELECT *
FROM ctx_group_message
WHERE group_id = sqlc.arg(group_id)
  AND NOT (actor_type = 'agent' AND actor_id = sqlc.arg(agent_id))
  AND btrim(content) = btrim(sqlc.arg(content))
ORDER BY seq DESC
LIMIT 1;

-- name: LastHumanSeqAtOrBefore :one
SELECT COALESCE(MAX(seq), 0)::bigint
FROM ctx_group_message
WHERE group_id = sqlc.arg(group_id)
  AND actor_type = 'human'
  AND seq <= sqlc.arg(trigger_seq);

-- name: CountAgentPostsSinceSeq :one
SELECT COUNT(*)::bigint
FROM ctx_group_message
WHERE group_id = sqlc.arg(group_id)
  AND actor_type = 'agent'
  AND seq > sqlc.arg(after_seq)
  AND delivery_state != 'failed';

-- name: CountAgentPostsInWindow :one
SELECT COUNT(*)::bigint
FROM ctx_group_message
WHERE group_id = sqlc.arg(group_id)
  AND actor_type = 'agent'
  AND actor_id = sqlc.arg(agent_id)
  AND created_at >= sqlc.arg(since)
  AND delivery_state != 'failed';

-- name: GetGroupLastActive :one
SELECT COALESCE(MAX(gm.created_at), gs.updated_at) AS last_active
FROM ctx_group_state gs
LEFT JOIN ctx_group_message gm ON gm.group_id = gs.id
WHERE gs.id = $1
GROUP BY gs.id;

-- name: ListGroupsByUser :many
SELECT
  gs.*,
  COALESCE(MAX(gm.created_at), gs.updated_at) AS last_active
FROM ctx_group_state gs
LEFT JOIN ctx_group_message gm ON gm.group_id = gs.id
WHERE gs.created_by_user_id = sqlc.arg(user_id)
  AND gs.platform = 'web'
GROUP BY gs.id
ORDER BY last_active DESC
LIMIT sqlc.arg(limit_count) OFFSET sqlc.arg(offset_count);

-- name: UpdateGroupName :one
UPDATE ctx_group_state
SET group_name = sqlc.arg(group_name), updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: SetGroupDispatchCaps :one
UPDATE ctx_group_state
SET agent_chain_hard_limit = sqlc.arg(agent_chain_hard_limit),
    max_agent_posts_per_minute = sqlc.arg(max_agent_posts_per_minute),
    max_replies_per_human_trigger = sqlc.arg(max_replies_per_human_trigger),
    hold_limit = sqlc.arg(hold_limit),
    updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: DeleteGroupState :exec
DELETE FROM ctx_group_state WHERE id = $1;

-- name: BumpGroupSeq :one
UPDATE ctx_group_state
SET next_seq = next_seq + 1,
    nudge_fallback_count = 0,
    updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING next_seq;

-- name: BumpGroupSeqWithoutNudgeFallbackReset :one
-- A fallback nudge is itself a canonical system message. It must not erase the
-- cap that bounds consecutive outage nudges; human and agent appends use the
-- regular bump above and reset the counter.
UPDATE ctx_group_state
SET next_seq = next_seq + 1,
    updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING next_seq;

-- name: ListGroupNudgeCandidate :many
SELECT gs.*, 
       (SELECT gm.id FROM ctx_group_message gm WHERE gm.group_id = gs.id ORDER BY gm.seq DESC LIMIT 1) AS last_message_id,
       (SELECT gm.actor_type FROM ctx_group_message gm WHERE gm.group_id = gs.id ORDER BY gm.seq DESC LIMIT 1) AS last_actor_type,
       (SELECT gm.actor_id FROM ctx_group_message gm WHERE gm.group_id = gs.id ORDER BY gm.seq DESC LIMIT 1) AS last_actor_id,
       (SELECT gm.content FROM ctx_group_message gm WHERE gm.group_id = gs.id ORDER BY gm.seq DESC LIMIT 1) AS last_content,
       (SELECT gm.created_at FROM ctx_group_message gm WHERE gm.group_id = gs.id ORDER BY gm.seq DESC LIMIT 1) AS last_message_at
FROM ctx_group_state gs
WHERE (SELECT gm.created_at FROM ctx_group_message gm WHERE gm.group_id = gs.id ORDER BY gm.seq DESC LIMIT 1) <= sqlc.arg(latest_before)
  AND (SELECT gm.created_at FROM ctx_group_message gm WHERE gm.group_id = gs.id ORDER BY gm.seq DESC LIMIT 1) >= sqlc.arg(earliest_after)
  AND (
    EXISTS (SELECT 1 FROM ctx_group_claim claim WHERE claim.group_id = gs.id AND claim.lease_until > sqlc.arg(now))
    OR (EXISTS (
      SELECT 1 FROM ctx_group_message human
      WHERE human.group_id = gs.id AND human.actor_type = 'human'
        AND human.seq = (SELECT MAX(gm.seq) FROM ctx_group_message gm WHERE gm.group_id = gs.id)
    ) AND NOT EXISTS (
      SELECT 1 FROM ctx_group_message reply
      WHERE reply.group_id = gs.id AND reply.actor_type = 'agent'
        AND reply.seq > (SELECT MAX(gm.seq) FROM ctx_group_message gm WHERE gm.group_id = gs.id)
        AND reply.delivery_state != 'failed'
    ))
  )
  -- Re-ask only when the answer could have changed: new activity since the last
  -- look, or a long backoff for the things that move on their own (claim leases,
  -- fallback budget). Classification costs a model call per candidate per tick.
  AND (
    gs.nudge_checked_at IS NULL
    OR gs.nudge_checked_at < (SELECT gm.created_at FROM ctx_group_message gm WHERE gm.group_id = gs.id ORDER BY gm.seq DESC LIMIT 1)
    OR gs.nudge_checked_at < sqlc.arg(recheck_before)
  )
ORDER BY (SELECT gm.created_at FROM ctx_group_message gm WHERE gm.group_id = gs.id ORDER BY gm.seq DESC LIMIT 1) ASC
LIMIT sqlc.arg(limit_count);

-- name: MarkGroupNudgeChecked :exec
UPDATE ctx_group_state
SET nudge_checked_at = sqlc.arg(now), updated_at = now()
WHERE id = sqlc.arg(group_id);

-- name: ClaimGroupNudge :one
UPDATE ctx_group_state
SET nudge_at = sqlc.arg(now), updated_at = now()
WHERE id = sqlc.arg(group_id)
  AND (nudge_at IS NULL OR nudge_at < sqlc.arg(cooldown_before))
RETURNING *;

-- name: IncrementGroupNudgeFallback :one
UPDATE ctx_group_state
SET nudge_fallback_count = nudge_fallback_count + 1, updated_at = now()
WHERE id = sqlc.arg(group_id)
  AND nudge_fallback_count < sqlc.arg(limit_count)
RETURNING *;

-- name: GetGroupMessageByPlatformID :one
SELECT * FROM ctx_group_message
WHERE group_id = sqlc.arg(group_id)
  AND platform_message_id = sqlc.arg(platform_message_id);

-- name: GetGroupMessageByIdempotencyKey :one
SELECT * FROM ctx_group_message
WHERE idempotency_key = sqlc.arg(idempotency_key);

-- name: GetGroupMessage :one
SELECT * FROM ctx_group_message WHERE id = $1;

-- content_blocks holds inbound image payloads, so a single row is bounded only
-- by the channel inline ceiling (telegram/qq inline up to 20MB today; 5MB once
-- #786 lands). Only the dispatch reads above (GetGroupMessage / the dedup
-- :one lookups) rehydrate images, so they keep SELECT *. The text-only list
-- consumers below — group-triage recent context, LCM cross-agent assembly,
-- and web pagination — read only the projected text columns, so they select an
-- explicit column list that EXCLUDES content_blocks and never drag image blobs
-- across a history window.

-- ListRecentGroupMessages currently has no caller; it keeps SELECT * so a future
-- replay/dispatch consumer that needs content_blocks gets the full row. Give it
-- an explicit lean list only if a text-only consumer adopts it.
-- name: ListRecentGroupMessages :many
SELECT * FROM ctx_group_message
WHERE group_id = sqlc.arg(group_id)
ORDER BY seq DESC
LIMIT sqlc.arg(max_count);

-- name: ListRecentGroupMessagesBeforeSeq :many
SELECT id, group_id, seq, source_channel_id, actor_type, actor_id,
       platform_message_id, reply_to, platform_timestamp, idempotency_key,
       content, reasoning, agent_session_id, created_at, delivery_state
FROM ctx_group_message
WHERE group_id = sqlc.arg(group_id)
  AND seq < sqlc.arg(before_seq)
ORDER BY seq DESC
LIMIT sqlc.arg(max_count);

-- name: ListGroupMessagesPaginated :many
SELECT id, group_id, seq, source_channel_id, actor_type, actor_id,
       platform_message_id, reply_to, platform_timestamp, idempotency_key,
       content, reasoning, agent_session_id, created_at, delivery_state
FROM ctx_group_message
WHERE group_id = sqlc.arg(group_id)
ORDER BY seq DESC
LIMIT sqlc.arg(limit_count) OFFSET sqlc.arg(offset_count);

-- name: CreateGroupMessage :one
INSERT INTO ctx_group_message (
  id, group_id, seq, source_channel_id, actor_type, actor_id,
  platform_message_id, reply_to, platform_timestamp, idempotency_key, content, content_blocks, reasoning, agent_session_id, delivery_state
)
VALUES (
  sqlc.arg(id), sqlc.arg(group_id), sqlc.arg(seq), sqlc.arg(source_channel_id),
  sqlc.arg(actor_type), sqlc.arg(actor_id), sqlc.arg(platform_message_id),
  sqlc.arg(reply_to), sqlc.arg(platform_timestamp), sqlc.arg(idempotency_key),
  sqlc.arg(content), COALESCE(sqlc.arg(content_blocks)::jsonb, '[]'::jsonb),
  sqlc.arg(reasoning), sqlc.arg(agent_session_id), sqlc.arg(delivery_state)
)
RETURNING *;

-- name: SetGroupMessageDeliveryState :one
UPDATE ctx_group_message
SET delivery_state = sqlc.arg(delivery_state)
WHERE id = sqlc.arg(id)
RETURNING *;
