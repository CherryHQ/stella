-- name: CreateKnowledgeFile :one
INSERT INTO knowledge_file (
    id,
    scope,
    user_id,
    agent_id,
    file_name,
    media_type,
    size_bytes,
    raw_sha256
) VALUES (
    sqlc.arg('id'),
    sqlc.arg('scope'),
    sqlc.narg('user_id'),
    sqlc.narg('agent_id'),
    sqlc.arg('file_name'),
    sqlc.arg('media_type'),
    sqlc.arg('size_bytes'),
    sqlc.arg('raw_sha256')
)
RETURNING *;

-- name: GetKnowledgeFile :one
SELECT
    id,
    scope,
    user_id,
    agent_id,
    file_name,
    media_type,
    size_bytes,
    raw_sha256,
    status,
    error_message,
    active_chunk_set_id,
    deleted_at,
    created_at,
    updated_at
FROM knowledge_file
WHERE id = sqlc.arg('id')
  AND deleted_at IS NULL;

-- name: GetKnowledgeRawOwners :many
SELECT id, deleted_at
FROM knowledge_file
WHERE id = ANY(sqlc.arg('ids')::uuid[]);

-- name: LockKnowledgeQuotaPool :exec
SELECT pg_advisory_xact_lock(sqlc.arg('lock_key')::bigint);

-- name: GetSystemKnowledgeQuotaUsage :one
SELECT
    count(*)::bigint AS used_files,
    coalesce(sum(size_bytes), 0)::bigint AS used_bytes
FROM knowledge_file
WHERE scope = 'system'
  AND deleted_at IS NULL;

-- name: GetSystemAgentKnowledgeQuotaUsage :one
SELECT
    count(*)::bigint AS used_files,
    coalesce(sum(size_bytes), 0)::bigint AS used_bytes
FROM knowledge_file
WHERE scope = 'system_agent'
  AND agent_id = sqlc.arg('agent_id')
  AND deleted_at IS NULL;

-- name: GetPersonalKnowledgeQuotaUsage :one
SELECT
    count(*)::bigint AS used_files,
    coalesce(sum(size_bytes), 0)::bigint AS used_bytes
FROM knowledge_file
WHERE scope IN ('user', 'user_agent')
  AND user_id = sqlc.arg('user_id')
  AND deleted_at IS NULL;
