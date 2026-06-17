-- name: CreateAgentTaskCriterion :one
INSERT INTO agent_task_criterion (id, task_id, description, required_flag, position, created_at)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: ListAgentTaskCriteria :many
SELECT * FROM agent_task_criterion WHERE task_id = $1 ORDER BY position;
