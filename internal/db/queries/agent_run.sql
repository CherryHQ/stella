-- name: CreateAgentRun :one
INSERT INTO agent_run (id, session_id, executor_boot_id, source, lease_expires_at)
VALUES (
    sqlc.arg(id), sqlc.arg(session_id), sqlc.arg(executor_boot_id), sqlc.arg(source),
    clock_timestamp() + make_interval(secs => sqlc.arg(lease_seconds)::integer)
)
ON CONFLICT DO NOTHING
RETURNING *;

-- name: InterruptExpiredAgentRunBySession :execrows
-- Lease expiry cannot prove that streaming or tool egress never crossed the
-- adapter boundary, even while completion is still open. Preserve unknown so
-- recovery never treats a crashed turn as safe to replay.
WITH terminal AS (
    UPDATE agent_run
    SET status = 'interrupted', terminal_reason = 'lease_expired',
        completion_state = 'unknown', completion_outcome = 'unknown',
        completion_status = 'interrupted', completion_reason = 'lease_expired',
        completion_ready_at = COALESCE(completion_ready_at, clock_timestamp()),
        completion_acked_at = NULL,
        completed_at = clock_timestamp(), lease_expires_at = clock_timestamp(), updated_at = clock_timestamp()
    WHERE agent_run.session_id = sqlc.arg(session_id)
      AND agent_run.status = 'running'
      AND agent_run.lease_expires_at <= clock_timestamp()
    RETURNING agent_run.session_id, agent_run.completion_outcome
)
UPDATE ctx_conversation c
SET last_turn_completed_at = clock_timestamp(),
    last_turn_result = 'error',
    updated_at = clock_timestamp()
FROM terminal
WHERE c.session_id = terminal.session_id
  AND c.archived = false;

-- name: GetAgentRun :one
SELECT * FROM agent_run WHERE id = $1;

-- name: GetRunningAgentRunBySession :one
SELECT * FROM agent_run WHERE session_id = $1 AND status = 'running';

-- name: LockAgentRunOwnership :one
SELECT id FROM agent_run
WHERE id = sqlc.arg(run_id)
  AND session_id = sqlc.arg(session_id)
  AND executor_boot_id = sqlc.arg(executor_boot_id)
  AND status = 'running'
  AND abort_requested_at IS NULL
  AND lease_expires_at > clock_timestamp()
FOR SHARE;

-- name: HeartbeatAgentRun :one
UPDATE agent_run
SET heartbeat_at = clock_timestamp(),
    lease_expires_at = clock_timestamp() + make_interval(secs => sqlc.arg(lease_seconds)::integer),
    updated_at = clock_timestamp()
WHERE id = sqlc.arg(run_id)
  AND executor_boot_id = sqlc.arg(executor_boot_id)
  AND status = 'running'
  AND abort_requested_at IS NULL
  AND lease_expires_at > clock_timestamp()
RETURNING *;

-- name: RequestSessionAgentRunAbort :one
UPDATE agent_run
SET abort_requested_at = clock_timestamp(), abort_reason = sqlc.arg(reason), updated_at = clock_timestamp()
WHERE session_id = sqlc.arg(session_id)
  AND status = 'running'
  AND abort_requested_at IS NULL
RETURNING *;

-- name: PrepareAgentRunCompletion :execrows
UPDATE agent_run
SET completion_state = 'ready', completion_status = sqlc.arg(status),
    completion_reason = sqlc.arg(reason), completion_ready_at = clock_timestamp(), updated_at = clock_timestamp()
WHERE id = sqlc.arg(run_id)
  AND executor_boot_id = sqlc.arg(executor_boot_id)
  AND status = 'running'
  AND completion_state = 'open'
  AND abort_requested_at IS NULL
  AND lease_expires_at > clock_timestamp();

-- name: CompleteAgentRunWithActivity :one
-- Couple the winning terminal transition and Session activity update. The
-- terminal row is returned even when the conversation was archived, so a
-- missing activity row cannot turn a durable completion into an ambiguous
-- lease result.
WITH terminal AS (
    UPDATE agent_run
    SET status = sqlc.arg(status), terminal_reason = sqlc.arg(reason),
        completion_state = 'acked', completion_outcome = sqlc.arg(completion_outcome),
        completion_status = sqlc.arg(status), completion_reason = sqlc.arg(reason),
        completion_ready_at = COALESCE(completion_ready_at, clock_timestamp()), completion_acked_at = clock_timestamp(),
        completed_at = clock_timestamp(), lease_expires_at = clock_timestamp(), updated_at = clock_timestamp()
    WHERE agent_run.id = sqlc.arg(run_id)
      AND agent_run.executor_boot_id = sqlc.arg(executor_boot_id)
      AND agent_run.status = 'running'
      AND agent_run.completion_state = 'open'
      AND agent_run.abort_requested_at IS NULL
      AND agent_run.lease_expires_at > clock_timestamp()
    RETURNING agent_run.session_id
), activity AS (
    UPDATE ctx_conversation c
    SET last_turn_completed_at = clock_timestamp(),
        last_turn_result = sqlc.arg(turn_result),
        updated_at = clock_timestamp()
    FROM terminal
    WHERE c.session_id = terminal.session_id
      AND c.archived = false
    RETURNING c.session_id
)
SELECT terminal.session_id
FROM terminal
LEFT JOIN activity ON activity.session_id = terminal.session_id;

-- name: AckAgentRunCompletionWithActivity :one
WITH terminal AS (
    UPDATE agent_run
    SET status = sqlc.arg(status),
        terminal_reason = completion_reason,
        completion_state = 'acked', completion_outcome = sqlc.arg(completion_outcome),
        completion_status = sqlc.arg(status),
        completion_ready_at = COALESCE(completion_ready_at, clock_timestamp()),
        completion_acked_at = clock_timestamp(), completed_at = clock_timestamp(), lease_expires_at = clock_timestamp(), updated_at = clock_timestamp()
    WHERE agent_run.id = sqlc.arg(run_id)
      AND agent_run.executor_boot_id = sqlc.arg(executor_boot_id)
      AND agent_run.status = 'running'
      AND (
          (agent_run.completion_state = 'ready' AND agent_run.completion_outcome = '')
          OR (agent_run.completion_state = 'open' AND sqlc.arg(completion_outcome) <> 'delivered')
      )
      AND agent_run.abort_requested_at IS NULL
      AND agent_run.lease_expires_at > clock_timestamp()
    RETURNING agent_run.session_id
), activity AS (
    UPDATE ctx_conversation c
    SET last_turn_completed_at = clock_timestamp(),
        last_turn_result = sqlc.arg(turn_result),
        updated_at = clock_timestamp()
    FROM terminal
    WHERE c.session_id = terminal.session_id
      AND c.archived = false
    RETURNING c.session_id
)
SELECT terminal.session_id
FROM terminal
LEFT JOIN activity ON activity.session_id = terminal.session_id;

-- name: AbortAgentRunWithActivity :one
WITH terminal AS (
    UPDATE agent_run
    SET status = 'aborted',
        abort_requested_at = COALESCE(abort_requested_at, clock_timestamp()),
        abort_reason = CASE WHEN abort_reason = '' THEN sqlc.arg(reason) ELSE abort_reason END,
        terminal_reason = CASE WHEN terminal_reason = '' THEN sqlc.arg(reason) ELSE terminal_reason END,
        completion_state = CASE WHEN completion_state = 'ready' THEN 'unknown' WHEN completion_state = 'open' THEN 'acked' ELSE completion_state END,
        completion_outcome = CASE WHEN completion_state = 'ready' THEN 'unknown' WHEN completion_state = 'open' THEN 'discarded' ELSE completion_outcome END,
        completion_status = 'aborted', completion_reason = CASE WHEN completion_reason = '' THEN sqlc.arg(reason) ELSE completion_reason END,
        completion_ready_at = COALESCE(completion_ready_at, clock_timestamp()),
        completion_acked_at = CASE WHEN completion_state = 'open' THEN clock_timestamp() ELSE completion_acked_at END,
        completed_at = clock_timestamp(), lease_expires_at = clock_timestamp(), updated_at = clock_timestamp()
    WHERE agent_run.id = sqlc.arg(run_id)
      AND agent_run.executor_boot_id = sqlc.arg(executor_boot_id)
      AND agent_run.status = 'running'
      AND agent_run.abort_requested_at IS NOT NULL
      AND agent_run.lease_expires_at > clock_timestamp()
    RETURNING agent_run.session_id, agent_run.completion_outcome
), activity AS (
    UPDATE ctx_conversation c
    SET last_turn_completed_at = clock_timestamp(),
        last_turn_result = 'canceled',
        updated_at = clock_timestamp()
    FROM terminal
    WHERE c.session_id = terminal.session_id
      AND c.archived = false
    RETURNING c.session_id
)
SELECT terminal.session_id
FROM terminal
LEFT JOIN activity ON activity.session_id = terminal.session_id;

-- name: ReapExpiredAgentRun :many
-- As above, every lease-expiry recovery is an unknown external outcome.
WITH candidates AS (
    SELECT id FROM agent_run
    WHERE status = 'running' AND lease_expires_at <= clock_timestamp()
    ORDER BY lease_expires_at
    LIMIT sqlc.arg(limit_count)
    FOR UPDATE SKIP LOCKED
), terminal AS (
    UPDATE agent_run run
    SET status = 'interrupted', terminal_reason = 'lease_expired',
        completion_state = 'unknown', completion_outcome = 'unknown',
        completion_status = 'interrupted', completion_reason = 'lease_expired',
        completion_ready_at = COALESCE(completion_ready_at, clock_timestamp()),
        completion_acked_at = NULL,
        completed_at = clock_timestamp(), lease_expires_at = clock_timestamp(), updated_at = clock_timestamp()
    FROM candidates
    WHERE run.id = candidates.id
    RETURNING run.*
), activity AS (
    UPDATE ctx_conversation c
    SET last_turn_completed_at = clock_timestamp(),
        last_turn_result = 'error',
        updated_at = clock_timestamp()
    FROM terminal
    WHERE c.session_id = terminal.session_id
      AND c.archived = false
    RETURNING c.session_id
)
SELECT terminal.*
FROM terminal
LEFT JOIN activity ON activity.session_id = terminal.session_id;

-- name: ReapAbortRequestedAgentRun :many
WITH candidates AS (
    SELECT id FROM agent_run
    WHERE status = 'running' AND abort_requested_at IS NOT NULL
    ORDER BY abort_requested_at
    LIMIT sqlc.arg(limit_count)
    FOR UPDATE SKIP LOCKED
), terminal AS (
    UPDATE agent_run run
    SET status = 'aborted', terminal_reason = abort_reason,
        completion_state = CASE WHEN completion_state = 'ready' THEN 'unknown' ELSE 'acked' END,
        completion_outcome = CASE WHEN completion_state = 'ready' THEN 'unknown' ELSE 'discarded' END,
        completion_status = 'aborted', completion_reason = abort_reason,
        completion_ready_at = COALESCE(completion_ready_at, clock_timestamp()),
        completion_acked_at = CASE WHEN completion_state = 'open' THEN clock_timestamp() ELSE completion_acked_at END,
        completed_at = clock_timestamp(), lease_expires_at = clock_timestamp(), updated_at = clock_timestamp()
    FROM candidates
    WHERE run.id = candidates.id
    RETURNING run.*
), activity AS (
    UPDATE ctx_conversation c
    SET last_turn_completed_at = clock_timestamp(),
        last_turn_result = 'canceled',
        updated_at = clock_timestamp()
    FROM terminal
    WHERE c.session_id = terminal.session_id
      AND c.archived = false
    RETURNING c.session_id
)
SELECT terminal.*
FROM terminal
LEFT JOIN activity ON activity.session_id = terminal.session_id;
