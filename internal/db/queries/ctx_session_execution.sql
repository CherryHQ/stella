-- name: ClaimSessionExecution :one
INSERT INTO ctx_session_execution (session_id, token, lease_until)
VALUES (sqlc.arg(session_id), sqlc.arg(token), clock_timestamp() + interval '30 seconds')
ON CONFLICT (session_id) DO UPDATE
SET token = EXCLUDED.token,
    lease_until = clock_timestamp() + interval '30 seconds',
    cancel_requested = false,
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
