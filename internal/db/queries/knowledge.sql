-- name: CreateKnowledgeFile :one
INSERT INTO knowledge_file (
    scope,
    user_id,
    agent_id,
    file_name,
    media_type,
    size_bytes,
    raw_content
) VALUES (
    sqlc.arg('scope'),
    sqlc.narg('user_id'),
    sqlc.narg('agent_id'),
    sqlc.arg('file_name'),
    sqlc.arg('media_type'),
    sqlc.arg('size_bytes'),
    sqlc.arg('raw_content')
)
RETURNING
    id,
    scope,
    user_id,
    agent_id,
    file_name,
    media_type,
    size_bytes,
    status,
    error_message,
    created_at,
    updated_at;

-- name: GetKnowledgeFile :one
SELECT
    id,
    scope,
    user_id,
    agent_id,
    file_name,
    media_type,
    size_bytes,
    status,
    error_message,
    created_at,
    updated_at
FROM knowledge_file
WHERE id = sqlc.arg('id');

-- name: GetKnowledgeFileForParse :one
SELECT
    id,
    media_type,
    raw_content,
    status
FROM knowledge_file
WHERE id = sqlc.arg('id');

-- name: GetKnowledgeFileStateForUpdate :one
SELECT id, status
FROM knowledge_file
WHERE id = sqlc.arg('id')
FOR UPDATE;

-- name: DeleteKnowledgeFile :one
DELETE FROM knowledge_file
WHERE id = sqlc.arg('id')
RETURNING
    id,
    scope,
    user_id,
    agent_id,
    file_name,
    media_type,
    size_bytes,
    status,
    error_message,
    created_at,
    updated_at;

-- name: ListKnowledgeFiles :many
SELECT
    id,
    scope,
    user_id,
    agent_id,
    file_name,
    media_type,
    size_bytes,
    status,
    error_message,
    created_at,
    updated_at
FROM knowledge_file
WHERE scope = sqlc.arg('scope')
  AND user_id IS NOT DISTINCT FROM sqlc.narg('user_id')::uuid
  AND agent_id IS NOT DISTINCT FROM sqlc.narg('agent_id')::text
  AND (
    sqlc.arg('q')::text = ''
    OR strpos(lower(file_name), lower(sqlc.arg('q')::text)) > 0
  )
  AND (
    sqlc.narg('cursor_created_at')::timestamptz IS NULL
    OR (created_at, id) < (
      sqlc.narg('cursor_created_at')::timestamptz,
      sqlc.narg('cursor_id')::uuid
    )
  )
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg('page_size');

-- name: LockKnowledgeQuotaPool :exec
SELECT pg_advisory_xact_lock(sqlc.arg('lock_key')::bigint);

-- name: GetSystemKnowledgeQuotaUsage :one
SELECT
    count(*)::bigint AS used_files,
    coalesce(sum(size_bytes), 0)::bigint AS used_bytes
FROM knowledge_file
WHERE scope = 'system';

-- name: GetSystemAgentKnowledgeQuotaUsage :one
SELECT
    count(*)::bigint AS used_files,
    coalesce(sum(size_bytes), 0)::bigint AS used_bytes
FROM knowledge_file
WHERE scope = 'system_agent'
  AND agent_id = sqlc.arg('agent_id');

-- name: GetPersonalKnowledgeQuotaUsage :one
SELECT
    count(*)::bigint AS used_files,
    coalesce(sum(size_bytes), 0)::bigint AS used_bytes
FROM knowledge_file
WHERE scope IN ('user', 'user_agent')
  AND user_id = sqlc.arg('user_id');

-- name: TouchProcessingKnowledgeFile :execrows
UPDATE knowledge_file
SET updated_at = now()
WHERE id = sqlc.arg('id')
  AND status = 'processing';

-- name: MarkKnowledgeFileReady :execrows
UPDATE knowledge_file
SET status = 'ready',
    error_message = NULL,
    updated_at = now()
WHERE id = sqlc.arg('id')
  AND status = 'processing';

-- name: MarkKnowledgeFileFailed :execrows
UPDATE knowledge_file
SET status = 'failed',
    error_message = sqlc.arg('error_message'),
    updated_at = now()
WHERE id = sqlc.arg('id')
  AND status = 'processing';

-- name: DeleteKnowledgeChunks :exec
DELETE FROM knowledge_chunk
WHERE file_id = sqlc.arg('file_id');

-- name: InsertKnowledgeChunks :execrows
INSERT INTO knowledge_chunk (file_id, ordinal, content, locator)
SELECT
    sqlc.arg('file_id')::uuid,
    input.ordinal,
    input.content,
    input.locator::jsonb
FROM ROWS FROM (
    unnest(sqlc.arg('ordinals')::bigint[]),
    unnest(sqlc.arg('contents')::text[]),
    unnest(sqlc.arg('locators')::text[])
) AS input(ordinal, content, locator);

-- name: ListStaleProcessingKnowledgeFiles :many
SELECT id, status, updated_at
FROM knowledge_file
WHERE status = 'processing'
  AND updated_at < sqlc.arg('stale_before')
ORDER BY updated_at ASC, id ASC
LIMIT sqlc.arg('limit');

-- name: SearchKnowledgeChunks :many
SELECT
    c.id,
    c.file_id,
    c.ordinal,
    c.content,
    c.locator,
    f.file_name,
    paradedb.score(c.id)::double precision AS score
FROM knowledge_chunk c
JOIN knowledge_file f ON f.id = c.file_id
WHERE c.id @@@ paradedb.match('content', sqlc.arg('match')::text)
  AND f.status = 'ready'
  AND (
    f.scope = 'system'
    OR (f.scope = 'system_agent' AND f.agent_id = sqlc.arg('agent_id'))
    OR (f.scope = 'user' AND f.user_id = sqlc.arg('user_id'))
    OR (
      f.scope = 'user_agent'
      AND f.user_id = sqlc.arg('user_id')
      AND f.agent_id = sqlc.arg('agent_id')
    )
  )
ORDER BY score DESC, c.id DESC
LIMIT sqlc.arg('limit');
