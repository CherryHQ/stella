-- Slice 3: goal queries.

-- name: CreateAgentGoal :one
INSERT INTO agent_goal (
    id, user_id, agent_id, project_id, title, description, status, priority,
    review_policy, context, output, created_at, updated_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetAgentGoal :one
SELECT * FROM agent_goal WHERE id = ?;

-- Dispatcher rollup scan. Skip quiescent goals (done/cancelled) so the bounded
-- window is spent only on goals rollup can still act on (running/blocked/failed
-- and pre-run states). failed is intentionally included - it is recoverable.
-- Archived goals are inert: excluding them stops rollup from silently recovering
-- an archived failed goal back to running while it stays hidden from default lists.
-- name: ListAgentGoals :many
SELECT * FROM agent_goal
WHERE status NOT IN ('done', 'cancelled')
  AND archived_at IS NULL
ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?;

-- name: ListAgentGoalsByUser :many
-- archived narg: NULL/false yields only active rows (archived_at IS NULL, the
-- default); true yields only archived rows (the history/restore view).
-- terminal narg: NULL matches any status; 1 keeps only terminal goals
-- (done/failed/cancelled, the history view); 0 keeps only non-terminal goals
-- (the active view). search narg is a case-insensitive substring matched against
-- title and description (NULL matches all).
SELECT * FROM agent_goal
WHERE user_id = sqlc.arg('user_id')
  AND (
    (sqlc.narg('archived') = 1 AND archived_at IS NOT NULL)
    OR (sqlc.narg('archived') IS NULL AND archived_at IS NULL)
    OR (sqlc.narg('archived') = 0 AND archived_at IS NULL)
  )
  AND (sqlc.narg('agent_id') IS NULL OR agent_id = sqlc.narg('agent_id'))
  AND (sqlc.narg('status') IS NULL OR status = sqlc.narg('status'))
  AND (sqlc.narg('project_id') IS NULL OR project_id = sqlc.narg('project_id'))
  AND (
    sqlc.narg('terminal') IS NULL
    OR (sqlc.narg('terminal') = 1 AND status IN ('done', 'failed', 'cancelled'))
    OR (sqlc.narg('terminal') = 0 AND status NOT IN ('done', 'failed', 'cancelled'))
  )
  AND (
    sqlc.narg('search') IS NULL
    OR instr(lower(title), lower(sqlc.narg('search'))) > 0
    OR instr(lower(description), lower(sqlc.narg('search'))) > 0
  )
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg('limit') OFFSET sqlc.arg('offset');

-- CountAgentGoalsByUser returns the total rows matching the same filters as
-- ListAgentGoalsByUser (ignoring pagination), so the UI can render numbered
-- pages and per-mode count badges from server-side state.
-- name: CountAgentGoalsByUser :one
SELECT COUNT(*) FROM agent_goal
WHERE user_id = sqlc.arg('user_id')
  AND (
    (sqlc.narg('archived') = 1 AND archived_at IS NOT NULL)
    OR (sqlc.narg('archived') IS NULL AND archived_at IS NULL)
    OR (sqlc.narg('archived') = 0 AND archived_at IS NULL)
  )
  AND (sqlc.narg('agent_id') IS NULL OR agent_id = sqlc.narg('agent_id'))
  AND (sqlc.narg('status') IS NULL OR status = sqlc.narg('status'))
  AND (sqlc.narg('project_id') IS NULL OR project_id = sqlc.narg('project_id'))
  AND (
    sqlc.narg('terminal') IS NULL
    OR (sqlc.narg('terminal') = 1 AND status IN ('done', 'failed', 'cancelled'))
    OR (sqlc.narg('terminal') = 0 AND status NOT IN ('done', 'failed', 'cancelled'))
  )
  AND (
    sqlc.narg('search') IS NULL
    OR instr(lower(title), lower(sqlc.narg('search'))) > 0
    OR instr(lower(description), lower(sqlc.narg('search'))) > 0
  );

-- name: ArchiveAgentGoal :execrows
UPDATE agent_goal SET archived_at = ?, updated_at = ? WHERE id = ? AND archived_at IS NULL;

-- name: UnarchiveAgentGoal :execrows
UPDATE agent_goal SET archived_at = NULL, updated_at = ? WHERE id = ? AND archived_at IS NOT NULL;

-- name: TransitionAgentGoalStatus :execrows
UPDATE agent_goal
SET status = ?, updated_at = ?
WHERE id = ? AND status = ?;

-- name: SetAgentGoalActiveReview :exec
UPDATE agent_goal SET active_review_id = ?, updated_at = ? WHERE id = ?;

-- name: SetAgentGoalOutput :exec
UPDATE agent_goal SET output = ?, completed_at = ?, updated_at = ? WHERE id = ?;

-- name: ListChildrenByGoal :many
SELECT * FROM agent_task WHERE goal_id = ? ORDER BY created_at ASC;

-- name: ListChildrenByGoalPaged :many
SELECT * FROM agent_task WHERE goal_id = ? ORDER BY created_at ASC, id ASC LIMIT ? OFFSET ?;

-- Aggregate counts of children by required_flag + status for rollup logic.
-- name: GoalChildCounts :one
SELECT
    COUNT(*)                                                    AS total,
    SUM(CASE WHEN required = 1 AND status = 'done'      THEN 1 ELSE 0 END) AS required_done,
    SUM(CASE WHEN required = 1 AND status = 'failed'    THEN 1 ELSE 0 END) AS required_failed,
    SUM(CASE WHEN required = 1 AND status = 'cancelled' THEN 1 ELSE 0 END) AS required_cancelled,
    SUM(CASE WHEN required = 1 AND status = 'blocked'   THEN 1 ELSE 0 END) AS required_blocked,
    SUM(CASE WHEN required = 1 AND status NOT IN ('done','failed','cancelled') THEN 1 ELSE 0 END) AS required_pending
FROM agent_task
WHERE goal_id = ?;

-- name: ListGoalPlanningCandidates :many
SELECT * FROM agent_goal
WHERE status = 'draft'
ORDER BY priority DESC, created_at ASC
LIMIT ?;

-- name: ListGoalSynthesisCandidates :many
SELECT * FROM agent_goal
WHERE status = 'running' AND review_policy != 'none'
ORDER BY priority DESC, updated_at ASC
LIMIT ?;
