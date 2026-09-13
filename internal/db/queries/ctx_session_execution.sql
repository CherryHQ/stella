-- name: LockSessionExecutionForClaim :one
-- Serializes claimants on the existing row and exposes the previous writer's
-- identity, so takeover of an expired lease can demand proof the old owner
-- exited before a new writer touches the session's writable environment.
SELECT * FROM ctx_session_execution
WHERE session_id = sqlc.arg(session_id)
FOR UPDATE;

-- name: ClaimSessionExecution :one
INSERT INTO ctx_session_execution (session_id, token, lease_until, owner_id, owner_host, owner_pid)
VALUES (sqlc.arg(session_id), sqlc.arg(token), clock_timestamp() + interval '30 seconds',
        sqlc.arg(owner_id), sqlc.arg(owner_host), sqlc.arg(owner_pid))
ON CONFLICT (session_id) DO UPDATE
SET token = EXCLUDED.token,
    lease_until = clock_timestamp() + interval '30 seconds',
    cancel_requested = false,
    owner_id = EXCLUDED.owner_id, owner_host = EXCLUDED.owner_host, owner_pid = EXCLUDED.owner_pid,
    created_at = clock_timestamp(), updated_at = clock_timestamp()
WHERE ctx_session_execution.lease_until <= clock_timestamp()
RETURNING *;

-- name: RenewSessionExecution :execrows
UPDATE ctx_session_execution
SET lease_until = clock_timestamp() + interval '30 seconds', updated_at = clock_timestamp()
WHERE session_id = sqlc.arg(session_id) AND token = sqlc.arg(token)
  AND lease_until > clock_timestamp() AND NOT cancel_requested;

-- name: ValidateSessionExecution :one
SELECT token FROM ctx_session_execution
WHERE session_id = sqlc.arg(session_id) AND token = sqlc.arg(token)
FOR SHARE;

-- name: GetSessionExecution :one
SELECT * FROM ctx_session_execution WHERE session_id = $1;

-- name: LockSessionExecutionForFinish :one
SELECT * FROM ctx_session_execution
WHERE session_id = sqlc.arg(session_id) AND token = sqlc.arg(token)
FOR UPDATE;

-- name: CancelSessionExecution :execrows
UPDATE ctx_session_execution SET cancel_requested = true, updated_at = clock_timestamp()
WHERE session_id = sqlc.arg(session_id) AND token = sqlc.arg(token)
  AND lease_until > clock_timestamp();

-- name: DeleteSessionExecution :execrows
DELETE FROM ctx_session_execution WHERE session_id = sqlc.arg(session_id) AND token = sqlc.arg(token);

-- name: DeleteExpiredSessionExecution :execrows
-- A fenced-out writer's exit attest: clears its own row only while it is
-- still expired, so a re-claimed lease is never touched.
DELETE FROM ctx_session_execution
WHERE session_id = sqlc.arg(session_id) AND token = sqlc.arg(token)
  AND lease_until <= clock_timestamp();

-- name: StartSessionExecutionActivity :execrows
UPDATE ctx_conversation
SET last_turn_started_at = clock_timestamp(), last_turn_result = NULL,
    last_active = clock_timestamp(), updated_at = clock_timestamp()
WHERE session_id = $1 AND archived = false;

-- name: FinishSessionExecutionActivity :exec
UPDATE ctx_conversation
SET last_turn_completed_at = clock_timestamp(), last_turn_result = sqlc.arg(result), updated_at = clock_timestamp()
WHERE session_id = sqlc.arg(session_id) AND last_turn_result IS NULL;

-- name: ListExpiredSessionExecutions :many
SELECT * FROM ctx_session_execution
WHERE lease_until <= clock_timestamp()
ORDER BY lease_until, session_id
LIMIT 100
FOR UPDATE SKIP LOCKED;

-- name: SessionExecutionValid :one
-- Call only after locking this exact token. clock_timestamp must be evaluated
-- after any row-lock wait, even when the waiting transaction changed no tuple.
SELECT (lease_until > clock_timestamp() AND (NOT cancel_requested OR sqlc.arg(allow_cancel)::boolean)) AS valid
FROM ctx_session_execution
WHERE session_id = sqlc.arg(session_id) AND token = sqlc.arg(token);

-- name: SetSessionExecutionRun :execrows
-- Link the lease to the run it covers; fenced by token.
UPDATE ctx_session_execution
SET run_id = sqlc.arg(run_id), updated_at = clock_timestamp()
WHERE session_id = sqlc.arg(session_id) AND token = sqlc.arg(token)
  AND lease_until > clock_timestamp();
