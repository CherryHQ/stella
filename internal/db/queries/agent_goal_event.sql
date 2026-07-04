-- name: AppendGoalEvent :one
-- Append-only goal timeline row. GoalService is the only writer.
INSERT INTO agent_goal_event (id, goal_id, attempt_id, event_type, payload)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: ListGoalEventByGoal :many
SELECT * FROM agent_goal_event
WHERE goal_id = sqlc.arg(goal_id)
ORDER BY created_at ASC, id ASC
LIMIT sqlc.arg('limit') OFFSET sqlc.arg('offset');

-- name: ListRecentGoalEventContext :many
-- Recent context events that matter to a new attempt prompt.
SELECT * FROM agent_goal_event
WHERE goal_id = sqlc.arg(goal_id)
  AND event_type IN ('attempt_finished', 'acceptance_recorded', 'human_message')
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg('limit');
