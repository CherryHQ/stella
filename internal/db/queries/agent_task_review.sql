-- name: CreateAgentTaskReview :one
INSERT INTO agent_task_review (
    id, user_id, task_id, reviewer_type, reviewer_id,
    submitted_run_id, reviewer_run_id, status,
    summary, feedback, created_at, resolved_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetAgentTaskReview :one
SELECT * FROM agent_task_review WHERE id = ? AND user_id = ?;

-- name: ListReviewsByTask :many
SELECT * FROM agent_task_review WHERE task_id = ? AND user_id = ?
ORDER BY created_at DESC;

-- name: GetActiveReviewByTask :one
SELECT * FROM agent_task_review
WHERE task_id = ? AND user_id = ? AND status = 'requested'
ORDER BY created_at DESC
LIMIT 1;

-- name: ResolveReview :exec
UPDATE agent_task_review
SET status = ?, summary = ?, feedback = ?, reviewer_run_id = ?, resolved_at = ?
WHERE id = ? AND user_id = ?;

-- name: CancelReviewsByTask :exec
UPDATE agent_task_review
SET status = 'cancelled', resolved_at = ?
WHERE task_id = ? AND user_id = ? AND status = 'requested';
