-- Slice 2: review queries.

-- name: CreateAgentReview :one
INSERT INTO agent_review (
    id, task_id, goal_id, submitted_run_id, reviewer_run_id,
    reviewer_type, reviewer_user_id, escalated_from_review_id,
    status, summary, feedback, created_at, updated_at
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
RETURNING *;

-- name: GetAgentReview :one
SELECT * FROM agent_review WHERE id = $1;

-- name: GetOpenReviewForTask :one
SELECT * FROM agent_review
WHERE task_id = $1 AND status IN ('requested','in_progress')
LIMIT 1;

-- name: ListAgentReviewsByTask :many
SELECT * FROM agent_review WHERE task_id = $1 ORDER BY created_at DESC, id DESC LIMIT $2 OFFSET $3;

-- name: ListAgentReviewsByGoal :many
SELECT * FROM agent_review WHERE goal_id = $1 ORDER BY created_at DESC, id DESC LIMIT $2 OFFSET $3;

-- Reviews awaiting agent dispatch: open, reviewer_type='agent', reviewer_run_id unset.
-- name: ListOpenAgentReviewsForDispatch :many
SELECT * FROM agent_review
WHERE status IN ('requested','in_progress')
  AND reviewer_type = 'agent'
  AND reviewer_run_id IS NULL
ORDER BY created_at ASC
LIMIT $1;

-- name: SetAgentReviewReviewerRun :execrows
UPDATE agent_review SET reviewer_run_id = $1, status = 'in_progress', updated_at = $2
WHERE id = $3 AND reviewer_run_id IS NULL;

-- name: SetAgentReviewDecision :execrows
UPDATE agent_review
SET status = $1, summary = $2, feedback = $3, resolved_at = $4, updated_at = $5
WHERE id = $6 AND status IN ('requested','in_progress');
