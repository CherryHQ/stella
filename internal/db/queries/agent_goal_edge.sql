-- name: CreateEdge :one
INSERT INTO agent_goal_edge (goal_id, upstream_id, edge_kind, on_failure)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetEdge :one
SELECT * FROM agent_goal_edge
WHERE goal_id = sqlc.arg(goal_id)
  AND upstream_id = sqlc.arg(upstream_id);

-- name: ListEdgeByGoal :many
SELECT * FROM agent_goal_edge
WHERE goal_id = sqlc.arg(goal_id)
ORDER BY created_at;


-- name: ListEdgeWithUpstreamState :many
SELECT
    e.*,
    u.lifecycle AS upstream_lifecycle,
    u.done_reason AS upstream_done_reason,
    u.accepted_output AS upstream_output
FROM agent_goal_edge e
JOIN agent_goal u ON u.id = e.upstream_id
WHERE e.goal_id = sqlc.arg(goal_id)
ORDER BY e.created_at;

-- name: WaiveEdge :exec
UPDATE agent_goal_edge
SET waived_at = now(),
    waived_by_user = sqlc.arg(waived_by_user),
    waiver_reason = sqlc.arg(waiver_reason)
WHERE goal_id = sqlc.arg(goal_id)
  AND upstream_id = sqlc.arg(upstream_id);
