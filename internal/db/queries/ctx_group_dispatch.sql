-- name: CreateGroupDispatch :exec
INSERT INTO ctx_group_dispatch (
  id, group_message_id, group_id, agent_id, reply_channel_id, status, attempt_count, lease_until, next_attempt_at, last_error, trigger_seq, kind
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10,
  (SELECT seq FROM ctx_group_message WHERE id = $2), 'wake') ON CONFLICT DO NOTHING;

-- name: CreateGroupWake :exec
INSERT INTO ctx_group_dispatch (
  id, group_message_id, group_id, agent_id, reply_channel_id, status, attempt_count, lease_until, next_attempt_at, last_error, trigger_seq, kind
)
VALUES ($1, $2, $3, $4, $5, 'pending', 0, NULL, NULL, '',
  (SELECT seq FROM ctx_group_message WHERE id = $2), 'wake') ON CONFLICT DO NOTHING;

-- name: CreateGroupNudge :exec
INSERT INTO ctx_group_dispatch (
  id, group_message_id, group_id, agent_id, reply_channel_id, status, attempt_count, lease_until, next_attempt_at, last_error, trigger_seq, kind
)
VALUES ($1, $2, $3, $4, $5, 'pending', 0, NULL, NULL, '',
  (SELECT seq FROM ctx_group_message WHERE id = $2), 'nudge') ON CONFLICT DO NOTHING;

-- name: GetGroupDispatch :one
SELECT * FROM ctx_group_dispatch WHERE id = $1;

-- name: CountGroupDispatchByMessage :one
SELECT CAST(COUNT(*) AS BIGINT) FROM ctx_group_dispatch
WHERE group_message_id = $1;

-- name: CountNonTerminalGroupDispatchByMessage :one
SELECT CAST(COUNT(*) AS BIGINT) FROM ctx_group_dispatch
WHERE group_message_id = $1
  AND status IN ('pending', 'running');

-- name: ListPendingGroupWakePairs :many
-- One representative per (group, agent) wakes the bounded pool. Claiming
-- chooses newest again transactionally, so this advisory feed may be stale.
SELECT DISTINCT ON (group_id, agent_id) *
FROM ctx_group_dispatch
WHERE kind = 'wake'
  AND status = 'pending'
  AND (next_attempt_at IS NULL OR next_attempt_at <= sqlc.arg('now'))
ORDER BY group_id, agent_id, trigger_seq DESC
LIMIT sqlc.arg(limit_count);

-- name: ListPendingGroupNudges :many
-- Nudge rows are targeted at one agent and never superseded, so they feed the
-- pool by id rather than through the (group, agent) high-water wake feed.
SELECT *
FROM ctx_group_dispatch
WHERE kind = 'nudge'
  AND status = 'pending'
  AND (next_attempt_at IS NULL OR next_attempt_at <= sqlc.arg('now'))
ORDER BY trigger_seq
LIMIT sqlc.arg(limit_count);

-- name: ClaimNewestGroupWake :one
-- Claim the current high-water wake and retire older pending snapshots in the
-- same transaction. A live sibling owns the agent's group session already.
WITH newest AS (
  SELECT id
  FROM ctx_group_dispatch candidate
  WHERE candidate.group_id = sqlc.arg(group_id)
    AND candidate.agent_id = sqlc.arg(agent_id)
    AND candidate.kind = 'wake'
    AND candidate.status = 'pending'
    AND (candidate.next_attempt_at IS NULL OR candidate.next_attempt_at <= sqlc.arg('now'))
    AND NOT EXISTS (
      SELECT 1
      FROM ctx_group_dispatch running
      WHERE running.group_id = candidate.group_id
        AND running.agent_id = candidate.agent_id
        AND running.kind = 'wake'
        AND running.status = 'running'
        AND running.lease_until IS NOT NULL
        AND running.lease_until > sqlc.arg('now')
    )
    -- A HOLD is only safe to retry once the wake snapshot covers the peer
    -- activity that caused it. ctx_group_chain_root scopes the gate to the
    -- current causal chain and is shared with CountHeldGroupDispatchesInChain.
    AND candidate.trigger_seq >= COALESCE((
      SELECT MAX(held.held_up_to_seq)
      FROM ctx_group_dispatch held
      WHERE held.group_id = candidate.group_id
        AND held.agent_id = candidate.agent_id
        -- Kind-agnostic on purpose: a held nudge is a yield like any other, and
        -- CountHeldGroupDispatchesInChain already spends budget for it. Counting
        -- it here too keeps one wake from re-running on a snapshot the agent has
        -- already been shown.
        AND held.status = 'held'
        AND held.trigger_seq >= ctx_group_chain_root(candidate.group_id, candidate.agent_id, candidate.trigger_seq)
    ), 0)
  ORDER BY candidate.trigger_seq DESC
  LIMIT 1
), supersede AS (
  UPDATE ctx_group_dispatch older
  SET status = 'superseded',
      lease_until = NULL,
      next_attempt_at = NULL,
      updated_at = now()
  WHERE older.group_id = sqlc.arg(group_id)
    AND older.agent_id = sqlc.arg(agent_id)
    AND older.kind = 'wake'
    AND older.status = 'pending'
    -- A row carrying an accepted result still owes egress: requeue preserves
    -- result_message_id, and nothing ever reads a superseded row, so retiring
    -- one here would strand a committed reply.
    AND (older.result_message_id IS NULL OR older.result_message_id = '')
    AND older.trigger_seq < (SELECT trigger_seq FROM ctx_group_dispatch WHERE id = (SELECT id FROM newest))
)
UPDATE ctx_group_dispatch dispatch
SET status = 'running',
    attempt_count = attempt_count + 1,
    lease_until = sqlc.arg(lease_until),
    next_attempt_at = NULL,
    last_error = '',
    updated_at = now()
-- The status guard is what makes the claim exclusive: a second claimer blocked
-- on the row lock re-checks only this qual, and `id = <constant>` still holds
-- against the tuple the winner just updated.
WHERE dispatch.id = (SELECT id FROM newest)
  AND dispatch.status = 'pending'
RETURNING dispatch.*;

-- name: MarkGroupDispatchHeld :execrows
UPDATE ctx_group_dispatch
SET status = 'held',
    lease_until = NULL,
    next_attempt_at = NULL,
    held_up_to_seq = sqlc.arg(held_up_to_seq),
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status = 'running'
  AND attempt_count = sqlc.arg(attempt_count);

-- name: MarkGroupDispatchSilent :execrows
UPDATE ctx_group_dispatch
SET status = 'silent',
    lease_until = NULL,
    next_attempt_at = NULL,
    last_error = sqlc.arg(reason),
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status = 'running'
  AND attempt_count = sqlc.arg(attempt_count);

-- name: CountHeldGroupDispatchesInChain :one
SELECT COUNT(*)::bigint
FROM ctx_group_dispatch held
WHERE held.group_id = sqlc.arg(group_id)
  AND held.agent_id = sqlc.arg(agent_id)
  AND held.status = 'held'
  -- The root human wake belongs to its own causal chain. A strict comparison
  -- would forget a HOLD on that first wake and let the same chain livelock.
  AND held.trigger_seq >= ctx_group_chain_root(sqlc.arg(group_id), sqlc.arg(agent_id), sqlc.arg(trigger_seq));

-- name: MaxHeldUpToSeqInChain :one
-- How far peers had moved when this agent was last held in the current chain.
-- Zero means it was never held here; the turn is a first attempt.
SELECT COALESCE(MAX(held.held_up_to_seq), 0)::bigint
FROM ctx_group_dispatch held
WHERE held.group_id = sqlc.arg(group_id)
  AND held.agent_id = sqlc.arg(agent_id)
  AND held.status = 'held'
  AND held.trigger_seq >= ctx_group_chain_root(sqlc.arg(group_id), sqlc.arg(agent_id), sqlc.arg(trigger_seq));

-- name: RequeueHeldGroupDispatchesAfterAcceptedPost :execrows
-- A peer may have yielded while this accepted post was pending platform egress.
-- If final delivery fails, that peer needs a fresh chance to answer instead of
-- leaving the human with neither reply.
UPDATE ctx_group_dispatch
SET status = 'pending',
    lease_until = NULL,
    next_attempt_at = NULL,
    held_up_to_seq = NULL,
    updated_at = now()
WHERE group_id = sqlc.arg(group_id)
  AND status = 'held'
  AND updated_at >= sqlc.arg(accepted_at);
-- name: ListExpiredRunningGroupDispatch :many
SELECT * FROM ctx_group_dispatch
WHERE status = 'running'
  AND lease_until IS NOT NULL
  AND lease_until <= sqlc.arg('now')
ORDER BY lease_until ASC
LIMIT sqlc.arg(limit_count);

-- name: ClaimPendingGroupDispatch :one
UPDATE ctx_group_dispatch
SET status = 'running',
    attempt_count = attempt_count + 1,
    lease_until = sqlc.arg(lease_until),
    next_attempt_at = NULL,
    last_error = '',
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status = 'pending'
  AND (next_attempt_at IS NULL OR next_attempt_at <= sqlc.arg('now'))
RETURNING *;

-- name: ClaimExpiredGroupDispatch :one
UPDATE ctx_group_dispatch
SET status = 'running',
    attempt_count = attempt_count + 1,
    lease_until = sqlc.arg(lease_until),
    next_attempt_at = NULL,
    last_error = '',
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status = 'running'
  AND lease_until IS NOT NULL
  AND lease_until <= sqlc.arg('now')
RETURNING *;

-- name: ExtendRunningGroupDispatchLease :execrows
UPDATE ctx_group_dispatch
SET lease_until = sqlc.arg(lease_until),
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status = 'running'
  AND attempt_count = sqlc.arg(attempt_count);

-- name: SetGroupDispatchResultMessage :execrows
UPDATE ctx_group_dispatch
SET result_message_id = sqlc.arg(result_message_id),
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status = 'running'
  AND attempt_count = sqlc.arg(attempt_count);

-- name: MarkGroupDispatchPublishStarted :execrows
-- Committed before the side effect so a crash is distinguishable from a publish
-- that never began. Cleared again when the publisher returns a real error.
UPDATE ctx_group_dispatch
SET publish_started_at = now(),
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status = 'running'
  AND attempt_count = sqlc.arg(attempt_count);

-- name: ClearGroupDispatchPublishStarted :execrows
UPDATE ctx_group_dispatch
SET publish_started_at = NULL,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND attempt_count = sqlc.arg(attempt_count);

-- name: MarkGroupDispatchPublished :execrows
UPDATE ctx_group_dispatch
SET published_at = now(),
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status = 'running'
  AND attempt_count = sqlc.arg(attempt_count)
  AND result_message_id = sqlc.arg(result_message_id)
  AND published_at IS NULL;

-- name: MarkGroupDispatchCompleted :execrows
UPDATE ctx_group_dispatch
SET status = 'completed',
    lease_until = NULL,
    next_attempt_at = NULL,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status = 'running'
  AND attempt_count = sqlc.arg(attempt_count);

-- name: MarkGroupDispatchFailed :execrows
UPDATE ctx_group_dispatch
SET status = 'failed',
    lease_until = NULL,
    next_attempt_at = NULL,
    last_error = sqlc.arg(last_error),
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status = 'running'
  AND attempt_count = sqlc.arg(attempt_count);

-- name: RequeueGroupDispatch :execrows
UPDATE ctx_group_dispatch
SET status = 'pending',
    lease_until = NULL,
    next_attempt_at = sqlc.arg(next_attempt_at),
    last_error = sqlc.arg(last_error),
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status = 'running'
  AND attempt_count = sqlc.arg(attempt_count);
