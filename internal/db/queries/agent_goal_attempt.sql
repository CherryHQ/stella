-- name: CreateAttempt :one
INSERT INTO agent_goal_attempt (
    id, goal_id, user_id, agent_id, executor_agent_id, session_id,
    purpose, attempt_no, status, input_context, lease_expires_at
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
RETURNING *;

-- name: GetAttempt :one
SELECT * FROM agent_goal_attempt WHERE id = $1;

-- name: GetMaxAttemptNo :one
-- Highest attempt_no for a goal within one purpose; 0 when none exist. The next
-- attempt_no is this + 1 (decomposition cannot derive it from attempt_count,
-- which only tracks execution attempts).
SELECT CAST(COALESCE(MAX(attempt_no), 0) AS BIGINT)
FROM agent_goal_attempt
WHERE goal_id = sqlc.arg(goal_id) AND purpose = sqlc.arg(purpose);

-- name: ListAttemptByGoal :many
SELECT * FROM agent_goal_attempt
WHERE goal_id = sqlc.arg(goal_id)
  AND (sqlc.narg(purpose)::text IS NULL OR purpose = sqlc.narg(purpose)::text)
ORDER BY attempt_no DESC;

-- name: GetActiveAttempt :one
SELECT * FROM agent_goal_attempt
WHERE goal_id = sqlc.arg(goal_id)
  AND purpose = sqlc.arg(purpose)
  AND status IN ('queued', 'running');

-- name: PromoteAttempt :execrows
UPDATE agent_goal_attempt
SET status = 'running',
    started_at = now(),
    heartbeat_at = now(),
    lease_expires_at = sqlc.arg(lease_expires_at),
    updated_at = now()
WHERE id = sqlc.arg(id) AND status = 'queued';

-- name: HeartbeatAttempt :execrows
UPDATE agent_goal_attempt
SET heartbeat_at = now(),
    lease_expires_at = sqlc.arg(lease_expires_at),
    updated_at = now()
WHERE id = sqlc.arg(id) AND status = 'running';

-- name: SubmitAttempt :execrows
UPDATE agent_goal_attempt
SET status = 'submitted',
    evidence = sqlc.arg(evidence),
    output = sqlc.arg(output),
    revision_id = sqlc.arg(revision_id),
    finished_at = now(),
    updated_at = now()
WHERE id = sqlc.arg(id) AND status = 'running';

-- name: SetAttemptGaps :exec
UPDATE agent_goal_attempt
SET gaps = sqlc.arg(gaps), updated_at = now()
WHERE id = sqlc.arg(id);

-- name: SetAttemptExecutor :exec
UPDATE agent_goal_attempt
SET executor_agent_id = sqlc.arg(executor_agent_id), updated_at = now()
WHERE id = sqlc.arg(id);

-- name: FinalizeAttempt :execrows
UPDATE agent_goal_attempt
SET status = sqlc.arg(to_status),
    error = sqlc.arg(error),
    finished_at = now(),
    updated_at = now()
WHERE id = sqlc.arg(id) AND status IN ('queued', 'running');

-- name: ListStaleAttempts :many
SELECT * FROM agent_goal_attempt
WHERE status IN ('queued', 'running')
  AND lease_expires_at IS NOT NULL
  AND lease_expires_at < sqlc.arg(now)
ORDER BY lease_expires_at ASC
LIMIT sqlc.arg('limit');

-- name: CountInflightAttemptsByRoot :one
SELECT CAST(COUNT(*) AS BIGINT)
FROM agent_goal_attempt a
JOIN agent_goal d ON d.id = a.goal_id
WHERE d.root_id = sqlc.arg(root_id)
  AND a.status IN ('queued', 'running');

-- name: CountInflightAttemptsByUser :one
SELECT CAST(COUNT(*) AS BIGINT)
FROM agent_goal_attempt a
WHERE a.user_id = sqlc.arg(user_id)
  AND a.status IN ('queued', 'running');
