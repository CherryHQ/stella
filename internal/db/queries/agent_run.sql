-- name: NextSessionEnqueueSeq :one
-- Caller must hold the session row lock so a later request can never overtake
-- an earlier non-terminal run.
SELECT (COALESCE(MAX(enqueue_seq), 0) + 1)::bigint FROM agent_run WHERE session_id = $1;

-- name: CreateAgentRun :one
-- Idempotent on request_key; a retry returns no row and the caller attaches
-- to the existing run instead of duplicating agent work.
INSERT INTO agent_run (inbox_id, session_id, agent_id, request_key, actor, input, reply_address, enqueue_seq, retry_of_run_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (request_key) DO NOTHING
RETURNING *;

-- name: GetAgentRun :one
SELECT * FROM agent_run WHERE id = $1;

-- name: GetAgentRunByRequestKey :one
SELECT * FROM agent_run WHERE request_key = $1;

-- name: ListQueuedAgentRuns :many
-- Worker claim scan: oldest first.
SELECT * FROM agent_run
WHERE state = 'queued'
ORDER BY enqueue_seq
LIMIT 50
FOR UPDATE SKIP LOCKED;

-- name: ListOpenAgentRunsBySession :many
SELECT * FROM agent_run
WHERE session_id = $1 AND state IN ('queued', 'running')
ORDER BY enqueue_seq;

-- name: ListRunningRunsByReplyChannel :many
-- Live-draft tailer scan: running runs whose frozen reply address targets
-- this channel. jsonb extraction, no index — the running set is tiny.
SELECT * FROM agent_run
WHERE state = 'running' AND reply_address->>'channel_id' = sqlc.arg(channel_id)::text
ORDER BY enqueue_seq
LIMIT 50;

-- name: StartAgentRun :execrows
-- Claim transitions queued -> running. The caller holds the session execution
-- token for this session inside the same transaction.
UPDATE agent_run
SET state = 'running', worker_id = sqlc.arg(worker_id),
    started_at = clock_timestamp(), updated_at = clock_timestamp()
WHERE id = sqlc.arg(id) AND state = 'queued';

-- name: FinishAgentRun :execrows
-- Terminal transition, fenced by worker_id and the running state: a fenced-out
-- worker cannot overwrite a newer state. Caller's tx also updates outbox.
UPDATE agent_run
SET state = sqlc.arg(state), error_code = sqlc.arg(error_code),
    finished_at = clock_timestamp(), updated_at = clock_timestamp()
WHERE id = sqlc.arg(id) AND worker_id = sqlc.arg(worker_id) AND state = 'running';

-- name: CancelQueuedAgentRun :execrows
UPDATE agent_run
SET state = 'canceled', finished_at = clock_timestamp(), updated_at = clock_timestamp()
WHERE id = $1 AND state = 'queued';

-- name: InterruptStaleRunningAgentRuns :execrows
-- A successful execution claim proves the previous owner is gone: any run
-- still 'running' for the session is orphaned and must not block the
-- one-running-per-session index. Done inside the claim transaction.
UPDATE agent_run
SET state = 'interrupted', finished_at = clock_timestamp(), updated_at = clock_timestamp()
WHERE session_id = $1 AND state = 'running';

-- name: ListExpiredRunningAgentRuns :many
-- Reaper scan: running runs whose execution lease died or vanished — a row
-- deleted by a fenced-out writer's exit attest (or stolen by a later claim)
-- orphans the run all the same.
-- r.* plus the lease token and owner so the same transaction can delete the
-- lease row — but only when the recorded writer is confirmed dead; a live or
-- unverifiable owner keeps its tombstone so takeover stays fenced (plan D9).
SELECT r.*, COALESCE(e.token::text, '')::text AS lease_token, e.owner_id, e.owner_host, e.owner_pid FROM agent_run r
LEFT JOIN ctx_session_execution e ON e.session_id = r.session_id AND e.run_id = r.id
WHERE r.state = 'running' AND (e.run_id IS NULL OR e.lease_until <= clock_timestamp())
ORDER BY r.enqueue_seq
LIMIT 100
FOR UPDATE OF r SKIP LOCKED;

-- name: MarkAgentRunInterrupted :execrows
UPDATE agent_run
SET state = 'interrupted', finished_at = clock_timestamp(), updated_at = clock_timestamp()
WHERE id = $1 AND state = 'running';

-- name: EarlierOpenAgentRunExists :one
-- Per-session head-of-line: a queued candidate may not claim while an
-- earlier non-terminal run for the same session exists.
SELECT EXISTS(
  SELECT 1 FROM agent_run
  WHERE session_id = $1 AND enqueue_seq < $2
    AND state = 'queued'
) AS exists;

-- name: FailQueuedAgentRun :execrows
-- Terminal failure at claim time (target gone/archived) — never executed.
UPDATE agent_run
SET state = 'failed', error_code = $2, finished_at = clock_timestamp(), updated_at = clock_timestamp()
WHERE id = $1 AND state = 'queued';

-- name: CancelQueuedAgentRunsBySession :execrows
UPDATE agent_run
SET state = 'canceled', finished_at = clock_timestamp(), updated_at = clock_timestamp()
WHERE session_id = $1 AND state = 'queued';
