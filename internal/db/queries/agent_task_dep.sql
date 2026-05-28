-- DAG edge queries. Cycle prevention is enforced at the service layer.

-- name: CreateAgentTaskDep :one
INSERT INTO agent_task_dep (task_id, dep_task_id, dep_kind, on_failure, created_at)
VALUES (?, ?, ?, ?, ?)
RETURNING *;

-- name: DeleteAgentTaskDep :exec
DELETE FROM agent_task_dep WHERE task_id = ? AND dep_task_id = ?;

-- name: ListAgentTaskDeps :many
SELECT * FROM agent_task_dep WHERE task_id = ?;

-- name: ListAgentTaskDependents :many
SELECT * FROM agent_task_dep WHERE dep_task_id = ?;

-- Returns each dep edge joined with the upstream task's status, so readiness
-- can be computed without N+1 queries.
-- name: ListAgentTaskDepsWithUpstream :many
SELECT
    sqlc.embed(d),
    t.status AS upstream_status
FROM agent_task_dep d
JOIN agent_task t ON t.id = d.dep_task_id
WHERE d.task_id = ?;

-- Waiver path for hard deps whose upstream failed/cancelled with on_failure=block.
-- name: WaiveAgentTaskDep :execrows
UPDATE agent_task_dep
SET waived_at = ?, waived_by_user = ?, waiver_reason = ?
WHERE task_id = ? AND dep_task_id = ? AND waived_at IS NULL;
