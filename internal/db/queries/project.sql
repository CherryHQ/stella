-- name: CreateProject :one
INSERT INTO project (id, agent_id, user_id, name, base_dir, description)
VALUES (?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetProject :one
SELECT * FROM project WHERE id = ? AND user_id = ?;

-- name: GetProjectByName :one
SELECT * FROM project WHERE agent_id = ? AND user_id = ? AND name = ?;

-- name: ListProjects :many
SELECT * FROM project WHERE agent_id = ? AND user_id = ? AND archived = 0 ORDER BY created_at ASC;

-- name: ListProjectsAll :many
SELECT * FROM project WHERE agent_id = ? AND user_id = ? ORDER BY created_at ASC;

-- name: UpdateProject :one
UPDATE project SET name = ?, description = ?, base_dir = ?, updated_at = datetime('now')
WHERE id = ? AND user_id = ?
RETURNING *;

-- name: ArchiveProject :exec
UPDATE project SET archived = ?, updated_at = datetime('now')
WHERE id = ? AND user_id = ?;

-- name: DeleteProject :exec
DELETE FROM project WHERE id = ? AND user_id = ?;
