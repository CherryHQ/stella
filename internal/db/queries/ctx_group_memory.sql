-- name: GetGroupMemory :one
SELECT * FROM ctx_group_memory WHERE group_id = $1;

-- name: UpsertGroupMemoryVersioned :one
INSERT INTO ctx_group_memory (group_id, content, version, updated_at)
VALUES ($1, $2, 1, now())
ON CONFLICT(group_id) DO UPDATE SET
    content = excluded.content,
    version = ctx_group_memory.version + 1,
    updated_at = now()
RETURNING *;
