-- name: CreateAcceptanceCriterion :one
INSERT INTO agent_task_acceptance_criterion (
    id, user_id, task_id, description, required, position, created_at
)
VALUES (?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: ListCriteriaByTask :many
SELECT * FROM agent_task_acceptance_criterion
WHERE task_id = ? AND user_id = ?
ORDER BY position ASC;

-- name: GetAcceptanceCriterion :one
SELECT * FROM agent_task_acceptance_criterion
WHERE id = ? AND user_id = ?;

-- name: DeleteAcceptanceCriterion :exec
DELETE FROM agent_task_acceptance_criterion
WHERE id = ? AND user_id = ?;

-- name: DeleteCriteriaByTask :exec
DELETE FROM agent_task_acceptance_criterion
WHERE task_id = ? AND user_id = ?;
