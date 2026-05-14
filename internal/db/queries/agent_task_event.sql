-- name: InsertAgentTaskEvent :one
INSERT INTO agent_task_event (id, task_id, event_type, detail, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: ListAgentTaskEvents :many
SELECT * FROM agent_task_event WHERE task_id = ? ORDER BY created_at ASC;
