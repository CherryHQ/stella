-- name: UpsertReviewItem :one
INSERT INTO agent_task_review_item (
    id, user_id, review_id, criterion_id, passed, evidence, created_at
)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (review_id, criterion_id) DO UPDATE
SET passed = excluded.passed, evidence = excluded.evidence
RETURNING *;

-- name: ListItemsByReview :many
SELECT * FROM agent_task_review_item
WHERE review_id = ? AND user_id = ?
ORDER BY created_at ASC;
