-- Append-only audit log.

-- name: InsertAgentTaskEvent :one
INSERT INTO agent_task_event (
    id, task_id, goal_id, run_id, blocker_id, review_id, event_type, from_status, to_status,
    actor_type, actor_id, detail, created_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: ListAgentTaskEvents :many
SELECT * FROM agent_task_event WHERE task_id = ? ORDER BY created_at ASC, id ASC LIMIT ? OFFSET ?;

-- name: ListAgentTaskEventsByGoal :many
SELECT * FROM agent_task_event WHERE goal_id = ? ORDER BY created_at ASC;

-- name: ListAgentTaskEventsByRun :many
SELECT * FROM agent_task_event WHERE run_id = ? ORDER BY created_at ASC;

-- GetLatestGoalArchiveDetail returns the detail JSON of a goal's most recent
-- goal_archive event. UnarchiveGoal reads the archived_task_ids it recorded so it
-- restores exactly the children that cascade-archived, not ones the user hid on
-- their own. Returns ErrNoRows for goals archived before that detail was recorded.
-- name: GetLatestGoalArchiveDetail :one
SELECT detail FROM agent_task_event
WHERE goal_id = ? AND event_type = 'goal_archive'
ORDER BY created_at DESC, id DESC
LIMIT 1;
