-- name: AppendAcceptanceEvent :one
-- Append-only ledger row: a deterministic check result or a judgment verdict.
-- Never updated/deleted in normal operation. The natural key
-- (deliverable, attempt, item, cache_key) is unique, so a re-submitted verdict
-- or a re-run check on identical inputs is a no-op (DO NOTHING): appending is
-- idempotent and returns sql.ErrNoRows on the duplicate, which the caller swallows.
INSERT INTO agent_dlv_acceptance_event (
    id,
    deliverable_id,
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
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(deliverable_id, attempt_id, item_id, cache_key) DO NOTHING
RETURNING *;

-- name: GetMaxAcceptanceSeq :one
-- Highest seq for a deliverable; -1 when no rows so next seq = result+1 = 0.
SELECT CAST(COALESCE(MAX(seq), -1) AS INTEGER)
FROM agent_dlv_acceptance_event
WHERE deliverable_id = sqlc.arg(deliverable_id);

-- name: ListAcceptanceEventByDeliverable :many
-- The projection fold: replay every event for a deliverable in seq order.
SELECT * FROM agent_dlv_acceptance_event
WHERE deliverable_id = sqlc.arg(deliverable_id)
ORDER BY seq ASC;

-- name: ListAcceptanceEventByAttempt :many
SELECT * FROM agent_dlv_acceptance_event
WHERE attempt_id = sqlc.arg(attempt_id)
ORDER BY seq ASC;

-- name: ProbeCheckCache :one
-- Check-result cache hit: the latest passing deterministic result for a cache_key.
-- Returns sql.ErrNoRows on a forced miss (no cached pass).
SELECT * FROM agent_dlv_acceptance_event
WHERE cache_key = sqlc.arg(cache_key)
  AND item_kind = 'deterministic'
  AND result = 'pass'
  AND cache_key != ''
ORDER BY created_at DESC
LIMIT 1;
