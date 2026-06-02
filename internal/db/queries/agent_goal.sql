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

-- name: ListAgentGoals :many
SELECT * FROM agent_goal ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?;

-- name: ListAgentGoalsByUser :many
SELECT * FROM agent_goal WHERE user_id = ? ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?;

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
    SUM(CASE WHEN required = 1 AND status IN ('done','failed','cancelled') THEN 0 ELSE 1 END) AS required_pending
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
