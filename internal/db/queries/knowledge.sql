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

-- name: LockKnowledgeFileLifecycle :one
SELECT
    id,
    media_type,
    size_bytes,
    raw_sha256,
    status,
    error_message,
    active_chunk_set_id,
    deleted_at,
    updated_at
FROM knowledge_file
WHERE id = sqlc.arg('id')
FOR UPDATE;

-- name: GetKnowledgeFileLifecycle :one
SELECT
    id,
    media_type,
    size_bytes,
    raw_sha256,
    status,
    error_message,
    active_chunk_set_id,
    deleted_at,
    updated_at
FROM knowledge_file
WHERE id = sqlc.arg('id');

-- name: CreateKnowledgeChunkSet :execrows
INSERT INTO knowledge_chunk_set (
    id,
    file_id,
    derivation_key,
    processor_key,
    raw_sha256
) VALUES (
    sqlc.arg('id'),
    sqlc.arg('file_id'),
    sqlc.arg('derivation_key'),
    sqlc.arg('processor_key'),
    sqlc.arg('raw_sha256')
)
ON CONFLICT (file_id, derivation_key) DO NOTHING;

-- name: GetKnowledgeChunkSetByDerivation :one
SELECT
    id,
    file_id,
    derivation_key,
    processor_key,
    raw_sha256,
    status,
    chunk_count,
    content_digest,
    error_message,
    created_at,
    updated_at,
    completed_at
FROM knowledge_chunk_set
WHERE file_id = sqlc.arg('file_id')
  AND derivation_key = sqlc.arg('derivation_key');

-- name: LockKnowledgeChunkSetLifecycle :one
SELECT
    id,
    file_id,
    derivation_key,
    processor_key,
    raw_sha256,
    status,
    chunk_count,
    content_digest,
    error_message,
    created_at,
    updated_at,
    completed_at
FROM knowledge_chunk_set
WHERE id = sqlc.arg('id')
FOR UPDATE;

-- name: InsertKnowledgeChunkBatch :execrows
INSERT INTO knowledge_chunk (
    id,
    chunk_set_id,
    ordinal,
    content,
    locator,
    content_sha256
)
SELECT
    input.id,
    sqlc.arg('chunk_set_id')::uuid,
    input.ordinal,
    input.content,
    input.locator::jsonb,
    input.content_sha256
FROM ROWS FROM (
    unnest(sqlc.arg('ids')::uuid[]),
    unnest(sqlc.arg('ordinals')::bigint[]),
    unnest(sqlc.arg('contents')::text[]),
    unnest(sqlc.arg('locators')::text[]),
    unnest(sqlc.arg('content_sha256s')::bytea[])
) AS input(id, ordinal, content, locator, content_sha256)
ON CONFLICT (chunk_set_id, ordinal) DO NOTHING;

-- name: ListKnowledgeChunkByOrdinals :many
SELECT ordinal, content, locator, content_sha256
FROM knowledge_chunk
WHERE chunk_set_id = sqlc.arg('chunk_set_id')
  AND ordinal = ANY(sqlc.arg('ordinals')::bigint[])
ORDER BY ordinal ASC;

-- name: TouchKnowledgeFileDerivation :execrows
UPDATE knowledge_file
SET updated_at = now()
WHERE id = sqlc.arg('id')
  AND deleted_at IS NULL;

-- name: GetKnowledgeChunkSetIntegrity :one
SELECT
    count(*)::bigint AS chunk_count,
    coalesce(min(ordinal), -1)::bigint AS min_ordinal,
    coalesce(max(ordinal), -1)::bigint AS max_ordinal,
    sha256(decode(coalesce(string_agg(
        lpad(to_hex(ordinal), 16, '0') || encode(content_sha256, 'hex'),
        '' ORDER BY ordinal
    ), ''), 'hex')) AS content_digest
FROM knowledge_chunk
WHERE chunk_set_id = sqlc.arg('chunk_set_id');

-- name: MarkKnowledgeChunkSetReady :execrows
UPDATE knowledge_chunk_set
SET status = 'ready',
    chunk_count = sqlc.arg('chunk_count'),
    content_digest = sqlc.arg('content_digest'),
    error_message = NULL,
    completed_at = now()
WHERE id = sqlc.arg('id')
  AND status = 'building';

-- name: PublishKnowledgeFileChunkSet :execrows
UPDATE knowledge_file
SET status = 'ready',
    error_message = NULL,
    active_chunk_set_id = sqlc.arg('chunk_set_id'),
    updated_at = now()
WHERE id = sqlc.arg('id')
  AND deleted_at IS NULL;

-- name: MarkKnowledgeChunkSetFailed :execrows
UPDATE knowledge_chunk_set
SET status = 'failed',
    error_message = sqlc.arg('error_message'),
    completed_at = now()
WHERE id = sqlc.arg('id')
  AND status = 'building';

-- name: MarkKnowledgeFileFailedWithoutActiveSet :execrows
UPDATE knowledge_file
SET status = 'failed',
    error_message = sqlc.arg('error_message'),
    updated_at = now()
WHERE id = sqlc.arg('id')
  AND active_chunk_set_id IS NULL
  AND deleted_at IS NULL;

-- name: TombstoneKnowledgeFile :execrows
UPDATE knowledge_file
SET deleted_at = now(),
    updated_at = now()
WHERE id = sqlc.arg('id')
  AND deleted_at IS NULL;

-- name: HardDeleteKnowledgeFile :execrows
DELETE FROM knowledge_file
WHERE id = sqlc.arg('id')
  AND deleted_at IS NOT NULL;

-- name: ListStaleKnowledgeDerivation :many
SELECT f.id, f.status, f.updated_at
FROM knowledge_file AS f
WHERE f.deleted_at IS NULL
  AND f.updated_at < sqlc.arg('stale_before')
  AND (
    f.status = 'processing'
    OR EXISTS (
      SELECT 1
      FROM knowledge_chunk_set AS chunk_set
      WHERE chunk_set.file_id = f.id
        AND chunk_set.status = 'building'
    )
  )
ORDER BY f.updated_at ASC, f.id ASC
LIMIT sqlc.arg('limit');

-- name: ListKnowledgeTombstone :many
SELECT id, deleted_at
FROM knowledge_file
WHERE deleted_at IS NOT NULL
ORDER BY deleted_at ASC, id ASC
LIMIT sqlc.arg('limit');

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
