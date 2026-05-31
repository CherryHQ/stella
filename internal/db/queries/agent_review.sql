-- Slice 2: review queries.

-- name: CreateAgentReview :one
INSERT INTO agent_review (
    id, task_id, goal_id, submitted_run_id, reviewer_run_id,
    reviewer_type, reviewer_user_id, escalated_from_review_id,
    status, summary, feedback, created_at, updated_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetAgentReview :one
SELECT * FROM agent_review WHERE id = ?;

-- name: GetOpenReviewForTask :one
SELECT * FROM agent_review
WHERE task_id = ? AND status IN ('requested','in_progress')
LIMIT 1;

-- name: ListAgentReviewsByTask :many
SELECT * FROM agent_review WHERE task_id = ? ORDER BY created_at DESC LIMIT ? OFFSET ?;

-- name: ListAgentReviewsByGoal :many
SELECT * FROM agent_review WHERE goal_id = ? ORDER BY created_at DESC LIMIT ? OFFSET ?;

-- Reviews awaiting agent dispatch: open, reviewer_type='agent', reviewer_run_id unset.
-- name: ListOpenAgentReviewsForDispatch :many
SELECT * FROM agent_review
WHERE status IN ('requested','in_progress')
  AND reviewer_type = 'agent'
  AND reviewer_run_id IS NULL
ORDER BY created_at ASC
LIMIT ?;

-- name: SetAgentReviewReviewerRun :execrows
UPDATE agent_review SET reviewer_run_id = ?, status = 'in_progress', updated_at = ?
WHERE id = ? AND reviewer_run_id IS NULL;

-- name: SetAgentReviewDecision :execrows
UPDATE agent_review
SET status = ?, summary = ?, feedback = ?, resolved_at = ?, updated_at = ?
WHERE id = ? AND status IN ('requested','in_progress');
