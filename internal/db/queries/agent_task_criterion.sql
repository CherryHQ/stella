-- name: CreateAgentTaskCriterion :one
INSERT INTO agent_task_criterion (id, task_id, description, required_flag, position, created_at)
VALUES (?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: ListAgentTaskCriteria :many
SELECT * FROM agent_task_criterion WHERE task_id = ? ORDER BY position;

-- DeleteAgentTaskCriteriaByTask clears a task's criteria so a replan reconcile
-- can replace them wholesale for a not-started task. #525.
-- name: DeleteAgentTaskCriteriaByTask :exec
DELETE FROM agent_task_criterion WHERE task_id = ?;
