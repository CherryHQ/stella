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

-- name: CountRanReviewAttemptsForOutput :one
-- Review attempts spent on the CURRENT needs_verdict episode: those created after
-- the reviewed execution attempt (a later execution attempt starts a fresh
-- episode) that actually ran (started_at set). A queued attempt reaped before it
-- was promoted never ran and does not charge the budget, mirroring the billable
-- budget predicate below. This is the agent-review budget; attempt_no is still
-- assigned globally per purpose via GetMaxAttemptNo.
SELECT COUNT(*)
FROM agent_goal_attempt
WHERE goal_id = sqlc.arg(goal_id)
  AND purpose = 'review'
  AND started_at IS NOT NULL
  AND created_at > sqlc.arg(since);

-- name: CountBillableAttempts :one
-- Budget spent for execution/decomposition convergence. In-flight attempts count
-- so a concurrent claim cannot overspend. Finished attempts count unless their
-- failure class is one old refund path: environment, contract, or flaky. Queued
-- reaps finalize as interrupted/flaky and are therefore not billable.
SELECT COUNT(*)
FROM agent_goal_attempt
WHERE goal_id = sqlc.arg(goal_id)
  AND purpose = sqlc.arg(purpose)
  AND (
        status IN ('queued', 'running', 'submitted')
        OR (status NOT IN ('queued', 'running', 'submitted') AND failure_class NOT IN ('environment', 'contract', 'flaky'))
      );

-- name: ListAttemptByGoal :many
SELECT * FROM agent_goal_attempt
WHERE goal_id = sqlc.arg(goal_id)
  AND (sqlc.narg(purpose)::text IS NULL OR purpose = sqlc.narg(purpose)::text)
ORDER BY attempt_no DESC;

-- name: ListAttemptSummaryByGoal :many
-- Agent-facing status only needs lightweight execution metadata. Keep the
-- large input/evidence/output/gaps payloads out of this bounded projection.
SELECT id, purpose, attempt_no, status, session_id, error, failure_class,
       started_at, finished_at, updated_at
FROM agent_goal_attempt
WHERE goal_id = sqlc.arg(goal_id)
ORDER BY attempt_no DESC
LIMIT sqlc.arg('limit');

-- name: GetActiveAttempt :one
SELECT * FROM agent_goal_attempt
WHERE goal_id = sqlc.arg(goal_id)
  AND purpose = sqlc.arg(purpose)
  AND status IN ('queued', 'running');

-- name: ListInflightAttemptsByGoal :many
-- Every queued/running attempt for a goal regardless of purpose. Cancel uses
-- this to finalize ALL in-flight work: only execution attempts are pointed by
-- active_attempt_id, so decomposition/review attempts would otherwise outlive
-- the cancel and later be reaped against a terminal goal.
SELECT * FROM agent_goal_attempt
WHERE goal_id = sqlc.arg(goal_id)
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
    finished_at = now(),
    updated_at = now()
WHERE id = sqlc.arg(id) AND status = 'running';

-- name: SetAttemptGaps :exec
UPDATE agent_goal_attempt
SET gaps = sqlc.arg(gaps), updated_at = now()
WHERE id = sqlc.arg(id);


-- name: SetAttemptRepairRounds :exec
UPDATE agent_goal_attempt
SET repair_rounds = sqlc.arg(repair_rounds), updated_at = now()
WHERE id = sqlc.arg(id);

-- name: FinalizeAttempt :execrows
UPDATE agent_goal_attempt
SET status = sqlc.arg(to_status),
    error = sqlc.arg(error),
    failure_class = sqlc.arg(failure_class),
    finished_at = now(),
    updated_at = now()
WHERE id = sqlc.arg(id) AND status IN ('queued', 'running');

-- name: ListStaleAttempts :many
-- Lease-expired attempts whose lease is a liveness signal. An attempt heartbeated
-- by a River worker (every execution attempt, and an autonomous decomposition
-- attempt) carries a forward-moving lease, so an expired lease means a genuine
-- orphan. Interactive decomposition attempts (planned in a session, never
-- enqueued/heartbeated) carry a NULL lease and are skipped by the IS NOT NULL
-- guard, so the reaper never bounces an active composite back to draft mid-planning.
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
