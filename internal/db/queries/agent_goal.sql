-- Slice 3: goal queries.

-- name: CreateAgentGoal :one
INSERT INTO agent_goal (
    id, user_id, agent_id, project_id, title, description, status, priority,
    review_policy, context, output, created_at, updated_at
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
RETURNING *;

-- name: GetAgentGoal :one
SELECT * FROM agent_goal WHERE id = $1;

-- Dispatcher rollup scan. Skip quiescent goals (done/cancelled) so the bounded
-- window is spent only on goals rollup can still act on (running/blocked/failed
-- and pre-run states). failed is intentionally included - it is recoverable.
-- name: ListAgentGoals :many
SELECT * FROM agent_goal
WHERE status NOT IN ('done', 'cancelled')
ORDER BY created_at DESC, id DESC LIMIT $1 OFFSET $2;

-- name: ListAgentGoalsByUser :many
SELECT * FROM agent_goal WHERE user_id = $1 ORDER BY created_at DESC, id DESC LIMIT $2 OFFSET $3;

-- name: ListAgentGoalsByUserAndAgent :many
SELECT * FROM agent_goal WHERE user_id = $1 AND agent_id = $2 ORDER BY created_at DESC, id DESC LIMIT $3 OFFSET $4;

-- name: TransitionAgentGoalStatus :execrows
UPDATE agent_goal
SET status = $1, updated_at = $2
WHERE id = $3 AND status = $4;

-- name: SetAgentGoalActiveReview :exec
UPDATE agent_goal SET active_review_id = $1, updated_at = $2 WHERE id = $3;

-- name: SetAgentGoalOutput :exec
UPDATE agent_goal SET output = $1, completed_at = $2, updated_at = $3 WHERE id = $4;

-- name: ListChildrenByGoal :many
SELECT * FROM agent_task WHERE goal_id = $1 ORDER BY created_at ASC;

-- name: ListChildrenByGoalPaged :many
SELECT * FROM agent_task WHERE goal_id = $1 ORDER BY created_at ASC, id ASC LIMIT $2 OFFSET $3;

-- Aggregate counts of children by required_flag + status for rollup logic.
-- name: GoalChildCounts :one
SELECT
    COUNT(*)                                                    AS total,
    SUM(CASE WHEN required = true AND status = 'done'      THEN 1 ELSE 0 END) AS required_done,
    SUM(CASE WHEN required = true AND status = 'failed'    THEN 1 ELSE 0 END) AS required_failed,
    SUM(CASE WHEN required = true AND status = 'cancelled' THEN 1 ELSE 0 END) AS required_cancelled,
    SUM(CASE WHEN required = true AND status = 'blocked'   THEN 1 ELSE 0 END) AS required_blocked,
    SUM(CASE WHEN required = true AND status NOT IN ('done','failed','cancelled') THEN 1 ELSE 0 END) AS required_pending
FROM agent_task
WHERE goal_id = $1;

-- name: ListGoalPlanningCandidates :many
SELECT * FROM agent_goal
WHERE status = 'draft'
ORDER BY priority DESC, created_at ASC
LIMIT $1;

-- name: ListGoalSynthesisCandidates :many
SELECT * FROM agent_goal
WHERE status = 'running' AND review_policy != 'none'
ORDER BY priority DESC, updated_at ASC
LIMIT $1;
