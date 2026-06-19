-- name: CreateEdge :one
INSERT INTO agent_dlv_edge (deliverable_id, upstream_id, edge_kind, on_failure)
VALUES (?, ?, ?, ?)
RETURNING *;

-- name: GetEdge :one
SELECT * FROM agent_dlv_edge
WHERE deliverable_id = sqlc.arg(deliverable_id)
  AND upstream_id = sqlc.arg(upstream_id);

-- name: ListEdgeByDeliverable :many
SELECT * FROM agent_dlv_edge
WHERE deliverable_id = sqlc.arg(deliverable_id)
ORDER BY created_at;

-- name: ListEdgeByUpstream :many
SELECT * FROM agent_dlv_edge
WHERE upstream_id = sqlc.arg(upstream_id)
ORDER BY created_at;

-- name: ListEdgeWithUpstreamState :many
SELECT
    e.*,
    u.lifecycle AS upstream_lifecycle,
    u.accepted_output AS upstream_output
FROM agent_dlv_edge e
JOIN agent_dlv_deliverable u ON u.id = e.upstream_id
WHERE e.deliverable_id = sqlc.arg(deliverable_id)
ORDER BY e.created_at;

-- name: WaiveEdge :exec
UPDATE agent_dlv_edge
SET waived_at = datetime('now'),
    waived_by_user = sqlc.arg(waived_by_user),
    waiver_reason = sqlc.arg(waiver_reason)
WHERE deliverable_id = sqlc.arg(deliverable_id)
  AND upstream_id = sqlc.arg(upstream_id);

-- name: DeleteEdge :exec
DELETE FROM agent_dlv_edge
WHERE deliverable_id = sqlc.arg(deliverable_id)
  AND upstream_id = sqlc.arg(upstream_id);
