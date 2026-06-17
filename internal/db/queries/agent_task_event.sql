-- Append-only audit log.

-- name: InsertAgentTaskEvent :one
INSERT INTO agent_task_event (
    id, task_id, goal_id, run_id, blocker_id, review_id, event_type, from_status, to_status,
    actor_type, actor_id, detail, created_at
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
RETURNING *;

-- name: ListAgentTaskEvents :many
SELECT * FROM agent_task_event WHERE task_id = $1 ORDER BY created_at ASC, id ASC LIMIT $2 OFFSET $3;

-- name: ListAgentTaskEventsByGoal :many
SELECT * FROM agent_task_event WHERE goal_id = $1 ORDER BY created_at ASC;

-- name: ListAgentTaskEventsByRun :many
SELECT * FROM agent_task_event WHERE run_id = $1 ORDER BY created_at ASC;
