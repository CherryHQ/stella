-- name: AppendAcceptanceEvent :one
-- Append-only ledger row: a deterministic check result or a judgment verdict.
-- Never updated/deleted in normal operation. The natural key
-- (goal, attempt, item, cache_key) is unique, so a re-submitted verdict
-- or a re-run check on identical inputs is a no-op (DO NOTHING): appending is
-- idempotent and returns pgx.ErrNoRows on the duplicate, which the caller swallows.
INSERT INTO agent_goal_acceptance_event (
    id,
    goal_id,
    attempt_id,
    seq,
    item_id,
    item_kind,
    result,
    command,
    exit_code,
    cache_key,
    authority,
    reviewer_user_id,
    reviewer_attempt_id,
    rationale,
    scope,
    scope_hash,
    detail
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
ON CONFLICT(goal_id, attempt_id, item_id, cache_key) DO NOTHING
RETURNING *;

-- name: GetMaxAcceptanceSeq :one
-- Highest seq for a goal; -1 when no rows so next seq = result+1 = 0.
SELECT CAST(COALESCE(MAX(seq), -1) AS BIGINT)
FROM agent_goal_acceptance_event
WHERE goal_id = sqlc.arg(goal_id);

-- name: ListAcceptanceEventByGoal :many
-- The projection fold: replay every event for a goal in seq order.
SELECT * FROM agent_goal_acceptance_event
WHERE goal_id = sqlc.arg(goal_id)
ORDER BY seq ASC;

-- name: ProbeCheckCache :one
-- Check-result cache hit: the latest passing deterministic result for a cache_key.
-- Returns pgx.ErrNoRows on a forced miss (no cached pass).
SELECT * FROM agent_goal_acceptance_event
WHERE cache_key = sqlc.arg(cache_key)
  AND item_kind = 'deterministic'
  AND result = 'pass'
  AND cache_key != ''
ORDER BY created_at DESC
LIMIT 1;
