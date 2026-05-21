-- name: InsertAgentTaskDep :exec
INSERT INTO agent_task_dep (task_id, dep_id)
VALUES (?, ?)
ON CONFLICT DO NOTHING;

-- name: DeleteAgentTaskDep :exec
DELETE FROM agent_task_dep WHERE task_id = ? AND dep_id = ?;

-- name: ListAgentTaskDeps :many
SELECT dep_id FROM agent_task_dep WHERE task_id = ? ORDER BY created_at ASC;

-- name: ListAgentTaskDependents :many
SELECT task_id FROM agent_task_dep WHERE dep_id = ? ORDER BY created_at ASC;

-- name: DeleteAgentTaskDepsByTask :exec
DELETE FROM agent_task_dep WHERE task_id = ?;
