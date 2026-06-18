-- agent_task v2 queries  Slice 1.
-- Status writes are split: only the transition service uses the *Status* /
-- *Active* / Claim mutators. Other code paths read via Get/List and write only
-- non-status fields via UpdateAgentTaskMeta.

-- name: CreateAgentTask :one
INSERT INTO agent_task (
    id, user_id, agent_id, session_id, goal_id, project_id, title, description, status, priority,
    required, retry_count, max_retries, not_before, deadline_at,
    context, output, created_at, updated_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- CreateAgentPlanTask is the materializer's create: a plan-backed work task that
-- carries source_plan_id + plan_item_id traceability. Public CreateTask must not
-- set these; work tasks come only from the materializer. #525.
-- name: CreateAgentPlanTask :one
INSERT INTO agent_task (
    id, user_id, agent_id, session_id, goal_id, project_id,
    source_plan_id, plan_item_id,
    title, description, status, priority,
    required, retry_count, max_retries, not_before, deadline_at,
    context, output, created_at, updated_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetAgentTask :one
SELECT * FROM agent_task WHERE id = ?;

-- ListAgentTaskBySourcePlan returns every task ever materialized from a plan
-- (including detached ones), so reconcile can diff the current plan against them. #525.
-- name: ListAgentTaskBySourcePlan :many
SELECT * FROM agent_task WHERE source_plan_id = ? ORDER BY created_at ASC, id ASC;

-- name: ListAgentTasks :many
SELECT * FROM agent_task ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?;

-- archived narg mirrors ListAgentGoalsByUser: NULL/false yields only active rows
-- (archived_at IS NULL, the default); true yields only archived rows (the
-- history/restore view).
-- name: ListAgentTasksByUser :many
SELECT * FROM agent_task
WHERE user_id = sqlc.arg('user_id')
  AND (
    (sqlc.narg('archived') = 1 AND archived_at IS NOT NULL)
    OR (sqlc.narg('archived') IS NULL AND archived_at IS NULL)
    OR (sqlc.narg('archived') = 0 AND archived_at IS NULL)
  )
  AND (sqlc.narg('agent_id') IS NULL OR agent_id = sqlc.narg('agent_id'))
  AND (sqlc.narg('status') IS NULL OR status = sqlc.narg('status'))
  AND (sqlc.narg('project_id') IS NULL OR project_id = sqlc.narg('project_id'))
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg('limit') OFFSET sqlc.arg('offset');

-- name: ListBlockedInboxTasks :many
SELECT
  t.id AS task_id,
  t.agent_id,
  t.project_id,
  t.title,
  b.question,
  b.created_at AS created_at
FROM agent_task t
JOIN agent_task_blocker b ON b.id = t.active_blocker_id
WHERE t.user_id = sqlc.arg(user_id)
  AND b.status = 'open'
  AND (sqlc.narg(agent_id) IS NULL OR t.agent_id = sqlc.narg(agent_id))

UNION ALL

SELECT
  t.id AS task_id,
  t.agent_id,
  t.project_id,
  t.title,
  b.question,
  b.created_at AS created_at
FROM agent_task t
JOIN agent_task_blocker b ON b.task_id = t.id AND b.status = 'open'
WHERE t.user_id = sqlc.arg(user_id)
  AND t.active_blocker_id IS NULL
  AND (sqlc.narg(agent_id) IS NULL OR t.agent_id = sqlc.narg(agent_id))
ORDER BY created_at DESC
LIMIT sqlc.arg(limit_count);

-- Only reviews waiting on a human belong in the inbox: agent-policy reviews
-- are dispatched to reviewer agents automatically, and a cancelled task can
-- leave its open review rows behind, so gate on t.status too.
-- name: ListReviewInboxTasks :many
SELECT
  t.id AS task_id,
  t.agent_id,
  t.project_id,
  t.title,
  r.id AS review_id,
  r.summary,
  r.created_at AS created_at
FROM agent_task t
JOIN agent_review r ON r.id = t.active_review_id
WHERE t.user_id = sqlc.arg(user_id)
  AND t.status = 'reviewing'
  AND r.status IN ('requested', 'in_progress')
  AND r.reviewer_type = 'human'
  AND (sqlc.narg(agent_id) IS NULL OR t.agent_id = sqlc.narg(agent_id))

UNION ALL

SELECT
  t.id AS task_id,
  t.agent_id,
  t.project_id,
  t.title,
  r.id AS review_id,
  r.summary,
  r.created_at AS created_at
FROM agent_task t
JOIN agent_review r ON r.task_id = t.id
  AND r.status IN ('requested', 'in_progress')
  AND r.reviewer_type = 'human'
WHERE t.user_id = sqlc.arg(user_id)
  AND t.status = 'reviewing'
  AND t.active_review_id IS NULL
  AND (sqlc.narg(agent_id) IS NULL OR t.agent_id = sqlc.narg(agent_id))
ORDER BY created_at DESC
LIMIT sqlc.arg(limit_count);

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

-- UpdateAgentTaskMetaIfPlannable edits a not-started task's definition during a
-- replan reconcile. The guard closes the claim race (codex BLOCKER 3): the
-- dispatcher can claim a ready task to running between the materializer's read
-- and write, so the update only lands while the task is still draft/ready with no
-- active run. 0 rows affected means it raced to running -> reconcile aborts with
-- ErrPlanItemInFlight. #525.
-- name: UpdateAgentTaskMetaIfPlannable :execrows
UPDATE agent_task
SET title = ?, description = ?, updated_at = ?
WHERE id = ? AND status IN ('draft', 'ready') AND active_run_id IS NULL;

-- SetAgentTaskDetached marks a removed-with-output plan task as detached: it
-- keeps source_plan_id/plan_item_id (traceability + handoff enforcement) but
-- drops out of rollup's required-child gate via required=0 + detached_at. #525.
-- name: SetAgentTaskDetached :exec
UPDATE agent_task
SET required = 0, detached_at = ?, updated_at = ?
WHERE id = ?;

-- Transition service uses these. Conditional UPDATE returns affected rows
-- so callers can detect lost races.

-- name: TransitionAgentTaskStatus :execrows
UPDATE agent_task
SET status = ?, updated_at = ?
WHERE id = ? AND status = ?;

-- Atomic claim: ready + no active run -> running. session_id is required at task creation.
-- name: ClaimAgentTask :execrows
UPDATE agent_task
SET status = 'running',
    active_run_id = ?,
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

-- name: ArchiveAgentTask :execrows
UPDATE agent_task SET archived_at = ?, updated_at = ? WHERE id = ? AND archived_at IS NULL;

-- name: UnarchiveAgentTask :execrows
UPDATE agent_task SET archived_at = NULL, updated_at = ? WHERE id = ? AND archived_at IS NOT NULL;
