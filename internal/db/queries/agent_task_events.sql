-- name: InsertAgentTaskEvent :one
INSERT INTO agent_task_events (id, task_id, event_type, detail, created_at)
VALUES (?, ?, ?, ?, ?)
RETURNING *;

-- name: ListAgentTaskEvents :many
SELECT * FROM agent_task_events WHERE task_id = ? ORDER BY created_at ASC;
