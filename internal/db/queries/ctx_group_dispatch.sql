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
WITH dispatch_lock AS (
  SELECT pg_advisory_xact_lock(hashtextextended(
    'group-dispatch:' || lock_key.group_id::text || ':' || lock_key.agent_id, 0
  ))
  FROM ctx_group_dispatch lock_key
  WHERE lock_key.group_id = sqlc.arg(group_id)
    AND lock_key.agent_id = sqlc.arg(agent_id)
  LIMIT 1
), newest AS (
  SELECT id
  FROM ctx_group_dispatch candidate
  CROSS JOIN dispatch_lock
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
        -- A nudge and a wake share the same group session. Either kind owns
        -- the one live turn, so this intentionally has no kind predicate.
        -- Expiry alone does not transfer ownership. The reaper must first move
        -- the old row out of running; its worker cancels before the proved lease
        -- expires. Otherwise a still-unwinding owner can overlap this claim.
        AND running.status = 'running'
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
    -- Keep the durable recovery class across a claim. It is what gives a
    -- lease-crashed accepted publish its separate 10-attempt ceiling.
    last_error = CASE
      WHEN last_error LIKE 'accepted_publish_recovery:%' THEN last_error
      ELSE ''
    END,
    updated_at = now()
FROM dispatch_lock
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
-- Once a covering successor commits its cursor, that HOLD is no longer owed and
-- must not keep bypassing peer-mention or lap triage on every later wake.
-- Zero means there is no unconsumed HOLD here; the turn is a first attempt.
SELECT COALESCE(MAX(held.held_up_to_seq), 0)::bigint
FROM ctx_group_dispatch held
WHERE held.group_id = sqlc.arg(group_id)
  AND held.agent_id = sqlc.arg(agent_id)
  AND held.status = 'held'
  AND held.trigger_seq >= ctx_group_chain_root(sqlc.arg(group_id), sqlc.arg(agent_id), sqlc.arg(trigger_seq))
  AND held.held_up_to_seq > COALESCE((
    SELECT cursor.last_seq
    FROM ctx_group_ingest_cursor cursor
    WHERE cursor.group_id = held.group_id
      AND cursor.pipeline = sqlc.arg(pipeline)
  ), 0);

-- name: RequeueHeldGroupDispatchesAfterAcceptedPost :execrows
-- Release only peers that actually yielded to this accepted post. Wall-clock
-- timestamps are not causal: clock precision and unrelated state writes can
-- otherwise release a hold from a different turn.
UPDATE ctx_group_dispatch
SET status = 'pending',
    lease_until = NULL,
    next_attempt_at = NULL,
    held_up_to_seq = NULL,
    updated_at = now()
WHERE group_id = sqlc.arg(group_id)
  AND status = 'held'
  AND trigger_seq < sqlc.arg(accepted_seq)
  AND held_up_to_seq >= sqlc.arg(accepted_seq);
-- name: ListExpiredRunningGroupDispatch :many
SELECT * FROM ctx_group_dispatch
WHERE status = 'running'
  AND lease_until IS NOT NULL
  AND lease_until <= sqlc.arg('now')
ORDER BY lease_until ASC
LIMIT sqlc.arg(limit_count);

-- name: ClaimPendingGroupNudge :one
-- The advisory lock serializes a nudge claim with ClaimNewestGroupWake for the
-- same (group, agent). Row locks alone cannot protect two distinct pending rows
-- from both observing no running sibling.
WITH dispatch_lock AS (
  SELECT pg_advisory_xact_lock(hashtextextended(
    'group-dispatch:' || lock_key.group_id::text || ':' || lock_key.agent_id, 0
  ))
  FROM ctx_group_dispatch lock_key
  WHERE lock_key.group_id = sqlc.arg(group_id)
    AND lock_key.agent_id = sqlc.arg(agent_id)
  LIMIT 1
)
UPDATE ctx_group_dispatch dispatch
SET status = 'running',
    attempt_count = attempt_count + 1,
    lease_until = sqlc.arg(lease_until),
    next_attempt_at = NULL,
    last_error = CASE
      WHEN last_error LIKE 'accepted_publish_recovery:%' THEN last_error
      ELSE ''
    END,
    updated_at = now()
FROM dispatch_lock
WHERE dispatch.id = sqlc.arg(id)
  AND dispatch.group_id = sqlc.arg(group_id)
  AND dispatch.agent_id = sqlc.arg(agent_id)
  AND dispatch.kind = 'nudge'
  AND dispatch.status = 'pending'
  AND (dispatch.next_attempt_at IS NULL OR dispatch.next_attempt_at <= sqlc.arg('now'))
  AND NOT EXISTS (
    SELECT 1
    FROM ctx_group_dispatch running
    WHERE running.group_id = dispatch.group_id
      AND running.agent_id = dispatch.agent_id
      AND running.status = 'running'
  )
RETURNING dispatch.*;

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

-- name: MarkExpiredGroupDispatchFailed :execrows
-- Re-check expiry in the terminal UPDATE. A heartbeat may have renewed the row
-- after the reaper listed it; that live owner must win over a stale snapshot.
UPDATE ctx_group_dispatch
SET status = 'failed',
    lease_until = NULL,
    next_attempt_at = NULL,
    last_error = sqlc.arg(last_error),
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status = 'running'
  AND attempt_count = sqlc.arg(attempt_count)
  AND lease_until IS NOT NULL
  AND lease_until <= sqlc.arg('now');

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

-- name: RequeueExpiredGroupDispatch :execrows
-- Re-check expiry in the UPDATE and delay the next claim by one heartbeat
-- interval, so the old owner observes its failed CAS and cancels before handoff.
UPDATE ctx_group_dispatch
SET status = 'pending',
    lease_until = NULL,
    next_attempt_at = sqlc.arg(next_attempt_at),
    last_error = sqlc.arg(last_error),
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status = 'running'
  AND attempt_count = sqlc.arg(attempt_count)
  AND lease_until IS NOT NULL
  AND lease_until <= sqlc.arg('now');

-- name: AgentMentionedSinceCursor :one
-- Was this agent addressed in any message it has not consumed yet?
-- Coalescing is what makes this necessary: the newest wake can supersede the
-- row created for the message that addressed the agent. The LCM cursor is the
-- incorporation boundary: accepted, PASS, held, and post-turn silent outcomes
-- have seen the mention, while a claim-time superseded row never ran. A held
-- successor is admitted separately from its durable held row in triage.
SELECT EXISTS (
  SELECT 1
  FROM ctx_group_outbox outbox
  JOIN ctx_group_message message ON message.id = outbox.group_message_id
  LEFT JOIN ctx_group_ingest_cursor cursor
    ON cursor.group_id = message.group_id
   AND cursor.pipeline = sqlc.arg(pipeline)
  WHERE message.group_id = sqlc.arg(group_id)
    AND message.actor_id <> sqlc.arg(agent_id)
    AND message.seq > COALESCE(cursor.last_seq, 0)
    AND message.seq <= sqlc.arg(trigger_seq)
    -- The key is the Go field name: pkg/channel.Mention pins it with an
    -- explicit `json:"AgentID"` tag precisely so this query keeps matching
    -- stored envelopes. Containment matches an element with that id whatever
    -- else it holds.
    AND (outbox.envelope::jsonb -> 'mentions') @> jsonb_build_array(jsonb_build_object('AgentID', sqlc.arg(agent_id)::text))
)::boolean;

-- name: AgentPostedSinceSeq :one
-- Has this agent already spoken after the given seq? A nudge names one agent
-- and never gets superseded, so a wake that was already in flight can post the
-- very reply the nudge asks for; without this the nudge posts a second one.
SELECT EXISTS (
  SELECT 1
  FROM ctx_group_message
  WHERE group_id = sqlc.arg(group_id)
    AND actor_type = 'agent'
    AND actor_id = sqlc.arg(agent_id)
    AND seq > sqlc.arg(after_seq)
    AND delivery_state <> 'failed'
)::boolean;

-- name: ListRunningGroupDispatchAgents :many
-- Presence snapshot for a fresh SSE subscriber: which members of this group are
-- executing a turn right now. Deliberately reads the durable row rather than an
-- in-process set, so a browser that opens mid-turn sees the same state a
-- long-lived subscriber does.
--
-- Known softness: a worker that died mid-turn leaves status='running' until its
-- lease expires (5min), so a snapshot can show a stale running. The reaper's
-- requeue then emits fresh frames, and every reconnect re-snapshots, so the UI
-- self-heals. Filtering by lease_until here would instead hide a live turn whose
-- heartbeat is merely late, which is the worse lie.
SELECT DISTINCT agent_id FROM ctx_group_dispatch
WHERE group_id = sqlc.arg(group_id)
  AND status = 'running'
ORDER BY agent_id;
