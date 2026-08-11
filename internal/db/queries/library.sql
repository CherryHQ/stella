-- name: CreateLibraryFile :one
INSERT INTO library_file (
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

-- name: GetLibraryFile :one
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
FROM library_file
WHERE id = sqlc.arg('id')
  AND deleted_at IS NULL;

-- name: ListManagedLibraryFiles :many
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
FROM library_file
WHERE scope = sqlc.arg('scope')
  AND user_id IS NOT DISTINCT FROM sqlc.narg('user_id')
  AND agent_id IS NOT DISTINCT FROM sqlc.narg('agent_id')
  AND deleted_at IS NULL
  AND (
    sqlc.arg('query')::text = ''
    OR strpos(lower(file_name), lower(sqlc.arg('query')::text)) > 0
  )
  AND (
    sqlc.narg('cursor_created_at')::timestamptz IS NULL
    OR (created_at, id) < (
      sqlc.narg('cursor_created_at')::timestamptz,
      sqlc.narg('cursor_id')::uuid
    )
  )
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg('limit');

-- name: LockLibraryFileLifecycle :one
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
FROM library_file
WHERE id = sqlc.arg('id')
FOR UPDATE;

-- name: GetLibraryFileLifecycle :one
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
FROM library_file
WHERE id = sqlc.arg('id');

-- name: CreateLibraryChunkSet :execrows
INSERT INTO library_chunk_set (
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

-- name: GetLibraryChunkSetByDerivation :one
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
FROM library_chunk_set
WHERE file_id = sqlc.arg('file_id')
  AND derivation_key = sqlc.arg('derivation_key');

-- name: LockLibraryChunkSetLifecycle :one
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
FROM library_chunk_set
WHERE id = sqlc.arg('id')
FOR UPDATE;

-- name: DeleteBuildingLibraryChunks :execrows
DELETE FROM library_chunk
WHERE chunk_set_id = sqlc.arg('chunk_set_id')
  AND EXISTS (
    SELECT 1
    FROM library_chunk_set
    WHERE id = sqlc.arg('chunk_set_id')
      AND status = 'building'
  );

-- name: InsertLibraryChunkBatch :execrows
INSERT INTO library_chunk (
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

-- name: TouchLibraryFileDerivation :execrows
UPDATE library_file
SET updated_at = now()
WHERE id = sqlc.arg('id')
  AND deleted_at IS NULL;

-- name: GetLibraryChunkSetIntegrity :one
SELECT
    count(*)::bigint AS chunk_count,
    coalesce(min(ordinal), -1)::bigint AS min_ordinal,
    coalesce(max(ordinal), -1)::bigint AS max_ordinal,
    sha256(decode(coalesce(string_agg(
        lpad(to_hex(ordinal), 16, '0') || encode(content_sha256, 'hex'),
        '' ORDER BY ordinal
    ), ''), 'hex')) AS content_digest
FROM library_chunk
WHERE chunk_set_id = sqlc.arg('chunk_set_id');

-- name: MarkLibraryChunkSetReady :execrows
UPDATE library_chunk_set
SET status = 'ready',
    chunk_count = sqlc.arg('chunk_count'),
    content_digest = sqlc.arg('content_digest'),
    error_message = NULL,
    updated_at = now(),
    completed_at = now()
WHERE id = sqlc.arg('id')
  AND status = 'building';

-- name: PublishLibraryFileChunkSet :execrows
UPDATE library_file
SET status = 'ready',
    error_message = NULL,
    active_chunk_set_id = sqlc.arg('chunk_set_id'),
    updated_at = now()
WHERE id = sqlc.arg('id')
  AND deleted_at IS NULL;

-- name: MarkLibraryChunkSetFailed :execrows
UPDATE library_chunk_set
SET status = 'failed',
    error_message = sqlc.arg('error_message'),
    updated_at = now(),
    completed_at = now()
WHERE id = sqlc.arg('id')
  AND status = 'building';

-- name: MarkLibraryFileFailedWithoutActiveSet :execrows
UPDATE library_file
SET status = 'failed',
    error_message = sqlc.arg('error_message'),
    updated_at = now()
WHERE id = sqlc.arg('id')
  AND active_chunk_set_id IS NULL
  AND deleted_at IS NULL;

-- name: TombstoneLibraryFile :execrows
UPDATE library_file
SET deleted_at = now(),
    updated_at = now()
WHERE id = sqlc.arg('id')
  AND deleted_at IS NULL;

-- name: HardDeleteLibraryFile :execrows
DELETE FROM library_file
WHERE id = sqlc.arg('id')
  AND deleted_at IS NOT NULL;

-- name: ListStaleLibraryDerivation :many
SELECT
  f.id,
  f.status,
  f.media_type,
  f.raw_sha256,
  f.updated_at,
  chunk_set.id AS chunk_set_id,
  chunk_set.derivation_key AS chunk_set_derivation_key,
  chunk_set.processor_key AS chunk_set_processor_key
FROM library_file AS f
LEFT JOIN library_chunk_set AS chunk_set
  ON chunk_set.file_id = f.id
 AND chunk_set.status = 'building'
WHERE f.deleted_at IS NULL
  AND f.updated_at < sqlc.arg('stale_before')
  AND (
    f.status = 'processing'
    OR chunk_set.id IS NOT NULL
  )
ORDER BY f.updated_at ASC, f.id ASC, chunk_set.created_at ASC, chunk_set.id ASC
LIMIT sqlc.arg('limit');

-- name: ListLibraryTombstone :many
SELECT id, deleted_at
FROM library_file
WHERE deleted_at IS NOT NULL
ORDER BY deleted_at ASC, id ASC
LIMIT sqlc.arg('limit');

-- name: GetLibraryRawOwners :many
SELECT id, deleted_at
FROM library_file
WHERE id = ANY(sqlc.arg('ids')::uuid[]);

-- name: LockLibraryQuotaPool :exec
SELECT pg_advisory_xact_lock(sqlc.arg('lock_key')::bigint);

-- name: GetSystemLibraryQuotaUsage :one
SELECT
    count(*)::bigint AS used_files,
    coalesce(sum(size_bytes), 0)::bigint AS used_bytes
FROM library_file
WHERE scope = 'system'
  AND deleted_at IS NULL;

-- name: GetSystemAgentLibraryQuotaUsage :one
SELECT
    count(*)::bigint AS used_files,
    coalesce(sum(size_bytes), 0)::bigint AS used_bytes
FROM library_file
WHERE scope = 'system_agent'
  AND agent_id = sqlc.arg('agent_id')
  AND deleted_at IS NULL;

-- name: GetPersonalLibraryQuotaUsage :one
SELECT
    count(*)::bigint AS used_files,
    coalesce(sum(size_bytes), 0)::bigint AS used_bytes
FROM library_file
WHERE scope IN ('user', 'user_agent')
  AND user_id = sqlc.arg('user_id')
  AND deleted_at IS NULL;
