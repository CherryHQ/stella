-- agent_task v2 queries  Slice 1.
-- Status writes are split: only the transition service uses the *Status* /
-- *Active* / Claim mutators. Other code paths read via Get/List and write only
-- non-status fields via UpdateAgentTaskMeta.

-- name: CreateAgentTask :one
INSERT INTO agent_task (
    id, user_id, agent_id, title, description, status, priority,
    required, retry_count, max_retries, not_before, deadline_at,
    session_id, context, output, created_at, updated_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetAgentTask :one
SELECT * FROM agent_task WHERE id = ?;

-- name: ListAgentTasks :many
SELECT * FROM agent_task ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?;

-- name: ListAgentTasksByUser :many
SELECT * FROM agent_task
WHERE user_id = sqlc.arg('user_id')
  AND (sqlc.narg('agent_id') IS NULL OR agent_id = sqlc.narg('agent_id'))
  AND (sqlc.narg('status') IS NULL OR status = sqlc.narg('status'))
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg('limit') OFFSET sqlc.arg('offset');

-- name: CountRunningAgentTasks :one
SELECT count(*) FROM agent_task WHERE status = 'running';

-- Coarse pre-filter for the dispatcher tick (HP4).
-- Real dispatchability is decided by readiness.Compute in Go.
-- name: ListReadyCandidates :many
SELECT * FROM agent_task
WHERE status = 'ready'
  AND active_run_id IS NULL
  AND (not_before IS NULL OR not_before <= ?)
ORDER BY priority DESC, created_at ASC
LIMIT ?;

-- name: UpdateAgentTaskMeta :exec
UPDATE agent_task
SET title = ?, description = ?, priority = ?, not_before = ?, deadline_at = ?,
    context = ?, updated_at = ?
WHERE id = ?;

-- Transition service uses these. Conditional UPDATE returns affected rows
-- so callers can detect lost races.

-- name: TransitionAgentTaskStatus :execrows
UPDATE agent_task
SET status = ?, updated_at = ?
WHERE id = ? AND status = ?;

-- Atomic claim: ready + no active run  running, set active_run_id and session_id.
-- name: ClaimAgentTask :execrows
UPDATE agent_task
SET status = 'running',
    active_run_id = ?,
    session_id = COALESCE(session_id, ?),
    updated_at = ?
WHERE id = ?
  AND status = 'ready'
  AND active_run_id IS NULL;

-- name: SetAgentTaskActiveRun :exec
UPDATE agent_task
SET active_run_id = ?, updated_at = ?
WHERE id = ?;

-- name: SetAgentTaskActiveBlocker :exec
UPDATE agent_task
SET active_blocker_id = ?, updated_at = ?
WHERE id = ?;

-- name: SetAgentTaskActiveReview :exec
UPDATE agent_task
SET active_review_id = ?, updated_at = ?
WHERE id = ?;

-- name: SetAgentTaskReviewPolicy :exec
UPDATE agent_task
SET review_policy = ?, updated_at = ?
WHERE id = ?;

-- name: IncrementAgentTaskRetry :exec
UPDATE agent_task
SET retry_count = retry_count + 1, updated_at = ?
WHERE id = ?;

-- name: SetAgentTaskOutput :exec
UPDATE agent_task
SET output = ?, completed_at = ?, updated_at = ?
WHERE id = ?;

-- name: SetAgentTaskCancelled :exec
UPDATE agent_task
SET cancelled_at = ?, updated_at = ?
WHERE id = ?;

-- name: ClearAgentTaskSession :exec
UPDATE agent_task
SET session_id = NULL, updated_at = ?
WHERE id = ?;

-- name: DeleteAgentTask :exec
DELETE FROM agent_task WHERE id = ?;
