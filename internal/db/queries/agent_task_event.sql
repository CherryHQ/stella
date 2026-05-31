-- Append-only audit log.

-- name: InsertAgentTaskEvent :one
INSERT INTO agent_task_event (
    id, task_id, goal_id, run_id, blocker_id, review_id, event_type, from_status, to_status,
    actor_type, actor_id, detail, created_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: ListAgentTaskEvents :many
SELECT * FROM agent_task_event WHERE task_id = ? ORDER BY created_at ASC LIMIT ? OFFSET ?;

-- name: ListAgentTaskEventsByGoal :many
SELECT * FROM agent_task_event WHERE goal_id = ? ORDER BY created_at ASC;

-- name: ListAgentTaskEventsByRun :many
SELECT * FROM agent_task_event WHERE run_id = ? ORDER BY created_at ASC;
