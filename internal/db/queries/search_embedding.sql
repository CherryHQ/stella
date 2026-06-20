-- name: UpsertSearchEmbedding :one
INSERT INTO search_embedding (owner_kind, owner_id, model, dims, content_hash, embedding)
VALUES (sqlc.arg(owner_kind), sqlc.arg(owner_id), sqlc.arg(model), sqlc.arg(dims), sqlc.arg(content_hash), sqlc.arg(embedding))
ON CONFLICT (owner_kind, owner_id, model) DO UPDATE
SET dims = excluded.dims,
    content_hash = excluded.content_hash,
    embedding = excluded.embedding,
    updated_at = now()
RETURNING *;

-- name: GetSearchEmbedding :one
SELECT * FROM search_embedding
WHERE owner_kind = sqlc.arg(owner_kind)
  AND owner_id = sqlc.arg(owner_id)
  AND model = sqlc.arg(model);

-- name: ListSearchEmbeddingByOwner :many
SELECT * FROM search_embedding
WHERE owner_kind = sqlc.arg(owner_kind)
  AND owner_id = sqlc.arg(owner_id)
ORDER BY model;

-- name: ListSearchEmbeddingByModel :many
SELECT * FROM search_embedding
WHERE model = sqlc.arg(model)
ORDER BY updated_at ASC, id ASC
LIMIT sqlc.arg('limit');

-- name: DeleteSearchEmbedding :exec
DELETE FROM search_embedding
WHERE owner_kind = sqlc.arg(owner_kind)
  AND owner_id = sqlc.arg(owner_id)
  AND model = sqlc.arg(model);

-- name: DeleteSearchEmbeddingByOwner :exec
DELETE FROM search_embedding
WHERE owner_kind = sqlc.arg(owner_kind)
  AND owner_id = sqlc.arg(owner_id);
