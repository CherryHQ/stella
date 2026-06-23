-- name: CreateKnowledge :one
INSERT INTO agent_knowledge (
    id,
    kind,
    scope,
    user_id,
    agent_id,
    name,
    content,
    status,
    evidence,
    confidence,
    expires_at,
    supersedes,
    metadata
)
VALUES (
    sqlc.arg(id),
    sqlc.arg(kind),
    sqlc.arg(scope),
    sqlc.narg(user_id),
    sqlc.narg(agent_id),
    sqlc.arg(name),
    sqlc.arg(content),
    sqlc.arg(status),
    sqlc.arg(evidence),
    sqlc.narg(confidence),
    sqlc.narg(expires_at),
    sqlc.narg(supersedes),
    sqlc.arg(metadata)
)
RETURNING *;

-- name: GetKnowledge :one
SELECT * FROM agent_knowledge
WHERE id = sqlc.arg(id);

-- name: ListActiveKnowledge :many
SELECT * FROM agent_knowledge
WHERE status = 'active'
  AND (expires_at IS NULL OR expires_at > now())
  AND (
    scope = 'system'
    OR (scope = 'system_agent' AND agent_id = sqlc.arg(agent_id))
    OR (scope = 'user'         AND user_id  = sqlc.arg(user_id))
    OR (scope = 'user_agent'   AND user_id  = sqlc.arg(user_id) AND agent_id = sqlc.arg(agent_id))
  )
  AND (sqlc.arg(kind)::text = '' OR kind = sqlc.arg(kind)::text)
ORDER BY updated_at DESC;

-- name: ListKnowledgeByScope :many
SELECT * FROM agent_knowledge
WHERE scope = sqlc.arg(scope)
  AND COALESCE(user_id::TEXT, '') = COALESCE(sqlc.narg(user_id)::TEXT, '')
  AND COALESCE(agent_id, '') = COALESCE(sqlc.narg(agent_id), '')
ORDER BY updated_at DESC;

-- name: ListKnowledgeByNameAndScope :many
SELECT * FROM agent_knowledge
WHERE name = sqlc.arg(name)
  AND status <> 'deprecated'
  AND scope = sqlc.arg(scope)
  AND COALESCE(user_id::TEXT, '') = COALESCE(sqlc.narg(user_id)::TEXT, '')
  AND COALESCE(agent_id, '') = COALESCE(sqlc.narg(agent_id), '')
ORDER BY updated_at DESC;

-- name: UpdateKnowledge :one
UPDATE agent_knowledge
SET name = sqlc.arg(name),
    content = sqlc.arg(content),
    status = sqlc.arg(status),
    evidence = sqlc.arg(evidence),
    confidence = sqlc.narg(confidence),
    expires_at = sqlc.narg(expires_at),
    supersedes = sqlc.narg(supersedes),
    metadata = sqlc.arg(metadata),
    updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: DeprecateKnowledge :exec
UPDATE agent_knowledge
SET status = 'deprecated',
    updated_at = now()
WHERE id = sqlc.arg(id);

-- name: ExpireKnowledgeDraftsByType :exec
UPDATE agent_knowledge
SET status = 'deprecated',
    updated_at = now()
WHERE status = 'draft'
  AND kind = sqlc.arg(kind)
  AND created_at < sqlc.arg(cutoff);
