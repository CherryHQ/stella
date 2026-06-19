-- name: CreateRevision :one
INSERT INTO agent_dlv_revision (
    id, deliverable_id, revision_no, status, review_policy, content, source_attempt_id, planning_session_id
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetRevision :one
SELECT * FROM agent_dlv_revision WHERE id = ?;

-- name: GetOpenRevision :one
SELECT * FROM agent_dlv_revision
WHERE deliverable_id = ? AND status IN ('draft', 'in_review');

-- name: GetMaterializedRevision :one
SELECT * FROM agent_dlv_revision
WHERE deliverable_id = ? AND materialized_at IS NOT NULL;

-- name: ListRevisionByDeliverable :many
SELECT * FROM agent_dlv_revision
WHERE deliverable_id = ?
ORDER BY revision_no DESC;

-- name: GetMaxRevisionNo :one
SELECT CAST(COALESCE(MAX(revision_no), 0) AS INTEGER)
FROM agent_dlv_revision
WHERE deliverable_id = ?;

-- name: UpdateRevisionStatus :execrows
UPDATE agent_dlv_revision
SET status = sqlc.arg(to_status), updated_at = datetime('now')
WHERE id = sqlc.arg(id) AND status = sqlc.arg(from_status);

-- name: UpdateRevisionContent :exec
UPDATE agent_dlv_revision
SET content = ?, updated_at = datetime('now')
WHERE id = ?;

-- name: AcceptRevision :execrows
UPDATE agent_dlv_revision
SET status = 'accepted', accepted_at = datetime('now'), updated_at = datetime('now')
WHERE id = ? AND status IN ('draft', 'in_review');

-- name: MaterializeRevision :execrows
UPDATE agent_dlv_revision
SET materialized_at = datetime('now'), updated_at = datetime('now')
WHERE id = ? AND accepted_at IS NOT NULL AND materialized_at IS NULL;

-- name: SupersedeOpenRevisions :exec
UPDATE agent_dlv_revision
SET status = 'superseded', updated_at = datetime('now')
WHERE deliverable_id = ? AND status IN ('draft', 'in_review');

-- name: SetRevisionPlanningSession :execrows
UPDATE agent_dlv_revision
SET planning_session_id = COALESCE(planning_session_id, sqlc.arg(planning_session_id)),
    updated_at = datetime('now')
WHERE id = sqlc.arg(id);
