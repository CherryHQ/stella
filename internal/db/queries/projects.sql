-- name: CreateProject :one
INSERT INTO projects (id, agent_id, user_id, name, base_dir, description, org_id)
VALUES (?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetProject :one
SELECT * FROM projects WHERE id = ? AND user_id = ?;

-- name: GetProjectByName :one
SELECT * FROM projects WHERE agent_id = ? AND user_id = ? AND name = ?;

-- name: ListProjects :many
SELECT * FROM projects WHERE agent_id = ? AND user_id = ? AND archived = 0 ORDER BY created_at ASC;

-- name: ListProjectsAll :many
SELECT * FROM projects WHERE agent_id = ? AND user_id = ? ORDER BY created_at ASC;

-- name: UpdateProject :one
UPDATE projects SET name = ?, description = ?, base_dir = ?, updated_at = datetime('now')
WHERE id = ? AND user_id = ?
RETURNING *;

-- name: ArchiveProject :exec
UPDATE projects SET archived = ?, updated_at = datetime('now')
WHERE id = ? AND user_id = ?;

-- name: DeleteProject :exec
DELETE FROM projects WHERE id = ? AND user_id = ?;
