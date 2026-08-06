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

-- name: DeleteGroupMemory :exec
DELETE FROM ctx_group_memory WHERE group_id = $1;

-- name: EnsureGroupMemoryVersion :one
INSERT INTO ctx_group_memory (group_id, content, version, updated_at)
VALUES (sqlc.arg(group_id), '', 0, now())
ON CONFLICT(group_id) DO UPDATE SET group_id = excluded.group_id
RETURNING version;

-- name: GetGroupMemoryVersion :one
SELECT version
FROM ctx_group_memory
WHERE group_id = sqlc.arg(group_id);

-- name: GetGroupMemoryVersionForUpdate :one
SELECT version
FROM ctx_group_memory
WHERE group_id = sqlc.arg(group_id)
FOR UPDATE;

-- name: UpdateGroupMemoryVersion :exec
UPDATE ctx_group_memory
SET version = sqlc.arg(version),
    updated_at = now()
WHERE group_id = sqlc.arg(group_id);
