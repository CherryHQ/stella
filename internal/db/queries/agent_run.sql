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
-- adapter boundary, so recovery always records the interruption as an unknown
-- external effect. Nothing replays it automatically.
WITH terminal AS (
    UPDATE agent_run
    SET status = 'interrupted', terminal_reason = 'lease_expired',
        completed_at = clock_timestamp(), lease_expires_at = clock_timestamp(), updated_at = clock_timestamp()
    WHERE agent_run.session_id = sqlc.arg(session_id)
      AND agent_run.status = 'running'
      AND agent_run.lease_expires_at <= clock_timestamp()
    RETURNING agent_run.session_id
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

-- name: CompleteAgentRunWithActivity :one
-- The single execution terminal transition, coupled with Session activity. A
-- durable abort request outranks the caller's status: the stop was recorded
-- before the owner learned its outcome, and the run is aborted either way. The
-- terminal row is returned even when the conversation was archived, so a
-- missing activity row cannot turn a durable completion into an ambiguous
-- result.
WITH terminal AS (
    UPDATE agent_run
    SET status = CASE WHEN agent_run.abort_requested_at IS NOT NULL THEN 'aborted' ELSE sqlc.arg(status) END,
        terminal_reason = CASE
            WHEN agent_run.abort_requested_at IS NOT NULL AND agent_run.abort_reason <> '' THEN agent_run.abort_reason
            ELSE sqlc.arg(reason)
        END,
        completed_at = clock_timestamp(), lease_expires_at = clock_timestamp(), updated_at = clock_timestamp()
    WHERE agent_run.id = sqlc.arg(run_id)
      AND agent_run.session_id = sqlc.arg(session_id)
      AND agent_run.executor_boot_id = sqlc.arg(executor_boot_id)
      AND agent_run.status = 'running'
      AND agent_run.lease_expires_at > clock_timestamp()
    RETURNING agent_run.session_id, agent_run.status
), activity AS (
    UPDATE ctx_conversation c
    SET last_turn_completed_at = clock_timestamp(),
        last_turn_result = CASE
            WHEN terminal.status = 'interrupted' THEN 'error'
            WHEN terminal.status IN ('canceled', 'aborted') THEN 'canceled'
            WHEN terminal.status = 'completed' THEN 'success'
            ELSE 'error'
        END,
        updated_at = clock_timestamp()
    FROM terminal
    WHERE c.session_id = terminal.session_id
      AND c.archived = false
    RETURNING c.session_id
)
SELECT terminal.session_id, terminal.status
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
    SET status = 'aborted', terminal_reason = run.abort_reason,
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
