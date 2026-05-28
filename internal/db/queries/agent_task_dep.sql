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

-- Reachable downstream tasks of a given task (Slice 4 reopen cascade).
-- Bounded by SQLite's default recursive limit; we additionally cap depth at 1000
-- in the CTE to prevent runaway cycles (cycles are forbidden by AddDep but the
-- CTE is defensive).
-- name: ListReachableDownstream :many
WITH RECURSIVE downstream(id, depth) AS (
    SELECT atd.task_id, 1 FROM agent_task_dep atd WHERE atd.dep_task_id = ?
    UNION
    SELECT atd.task_id, ds.depth + 1
    FROM agent_task_dep atd
    JOIN downstream ds ON atd.dep_task_id = ds.id
    WHERE ds.depth < 1000
)
SELECT t.* FROM agent_task t JOIN downstream ds ON t.id = ds.id;

-- Waiver path for hard deps whose upstream failed/cancelled with on_failure=block.
-- name: WaiveAgentTaskDep :execrows
UPDATE agent_task_dep
SET waived_at = ?, waived_by_user = ?, waiver_reason = ?
WHERE task_id = ? AND dep_task_id = ? AND waived_at IS NULL;
