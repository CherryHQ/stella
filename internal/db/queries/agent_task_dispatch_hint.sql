-- Dispatch hint persistence between task creation and the next claim (B1 / D13).

-- name: CreateAgentTaskDispatchHint :one
INSERT INTO agent_task_dispatch_hint (id, task_id, kind, executor_agent_id, created_at)
VALUES (?, ?, ?, ?, ?)
RETURNING *;

-- name: GetLiveDispatchHintForTask :one
SELECT * FROM agent_task_dispatch_hint
WHERE task_id = ? AND kind = ? AND consumed_at IS NULL
LIMIT 1;

-- name: ConsumeDispatchHint :execrows
UPDATE agent_task_dispatch_hint
SET consumed_at = ?
WHERE id = ? AND consumed_at IS NULL;

-- name: DeleteDispatchHint :exec
DELETE FROM agent_task_dispatch_hint WHERE id = ?;
