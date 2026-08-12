-- name: CreateProject :one
INSERT INTO project (id, agent_id, user_id, name, base_dir, description)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: GetProject :one
SELECT * FROM project WHERE id = $1 AND user_id = $2;

-- name: GetProjectByOwner :one
SELECT * FROM project WHERE id = $1 AND user_id = $2 AND agent_id = $3;

-- name: GetProjectByName :one
SELECT * FROM project WHERE agent_id = $1 AND user_id = $2 AND name = $3;

-- name: ListProjects :many
SELECT * FROM project WHERE agent_id = $1 AND user_id = $2 AND archived = false ORDER BY created_at ASC;

-- name: ListProjectsAll :many
SELECT * FROM project WHERE agent_id = $1 AND user_id = $2 ORDER BY created_at ASC;

-- name: UpdateProject :one
UPDATE project SET name = $1, description = $2, base_dir = $3, updated_at = now()
WHERE id = $4 AND user_id = $5
RETURNING *;

-- name: ArchiveProject :exec
UPDATE project SET archived = $1, updated_at = now()
WHERE id = $2 AND user_id = $3;

-- name: DeleteProject :exec
DELETE FROM project WHERE id = $1 AND user_id = $2;
