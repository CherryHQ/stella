-- name: CreateRevision :one
INSERT INTO agent_goal_revision (
    id, goal_id, revision_no, status, review_policy, content, source_attempt_id, planning_session_id
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: GetRevision :one
SELECT * FROM agent_goal_revision WHERE id = $1;

-- name: GetOpenRevision :one
SELECT * FROM agent_goal_revision
WHERE goal_id = $1 AND status IN ('draft', 'in_review');

-- name: GetMaterializedRevision :one
SELECT * FROM agent_goal_revision
WHERE goal_id = $1 AND materialized_at IS NOT NULL;

-- name: ListRevisionByGoal :many
SELECT * FROM agent_goal_revision
WHERE goal_id = $1
ORDER BY revision_no DESC;

-- name: GetMaxRevisionNo :one
SELECT CAST(COALESCE(MAX(revision_no), 0) AS BIGINT)
FROM agent_goal_revision
WHERE goal_id = $1;

-- name: UpdateRevisionStatus :execrows
UPDATE agent_goal_revision
SET status = sqlc.arg(to_status), updated_at = now()
WHERE id = sqlc.arg(id) AND status = sqlc.arg(from_status);

-- name: UpdateRevisionContent :exec
UPDATE agent_goal_revision
SET content = $1, updated_at = now()
WHERE id = $2;

-- name: AcceptRevision :execrows
UPDATE agent_goal_revision
SET status = 'accepted', accepted_at = now(), updated_at = now()
WHERE id = $1 AND status IN ('draft', 'in_review');

-- name: MaterializeRevision :execrows
UPDATE agent_goal_revision
SET materialized_at = now(), updated_at = now()
WHERE id = $1 AND accepted_at IS NOT NULL AND materialized_at IS NULL;

-- name: SupersedeOpenRevisions :exec
UPDATE agent_goal_revision
SET status = 'superseded', updated_at = now()
WHERE goal_id = $1 AND status IN ('draft', 'in_review');

-- name: SetRevisionPlanningSession :execrows
UPDATE agent_goal_revision
SET planning_session_id = COALESCE(planning_session_id, sqlc.arg(planning_session_id)),
    updated_at = now()
WHERE id = sqlc.arg(id);
