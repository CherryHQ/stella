-- name: CreateConversation :one
INSERT INTO ctx_conversation (id, session_id, title, channel, kind, project_id, archived, last_active, agent_id, user_id, group_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, sqlc.narg(group_id))
RETURNING *;

-- name: GetConversation :one
SELECT * FROM ctx_conversation
WHERE id = sqlc.arg(id)
  AND user_id = sqlc.arg(user_id)
  AND agent_id IS NOT DISTINCT FROM sqlc.narg(agent_id);

-- name: GetConversationBySessionID :one
SELECT * FROM ctx_conversation
WHERE session_id = sqlc.arg(session_id)
  AND user_id = sqlc.arg(user_id)
  AND agent_id IS NOT DISTINCT FROM sqlc.narg(agent_id);

-- name: GetConversationAgentBySessionID :one
SELECT agent_id FROM ctx_conversation
WHERE session_id = sqlc.arg(session_id)
  AND user_id = sqlc.arg(user_id);

-- name: UpdateConversationTitle :exec
UPDATE ctx_conversation SET title = sqlc.arg(title), updated_at = now()
WHERE id = sqlc.arg(id) AND user_id = sqlc.arg(user_id) AND agent_id IS NOT DISTINCT FROM sqlc.narg(agent_id);

-- name: UpdateConversationBootstrapped :exec
UPDATE ctx_conversation SET bootstrapped_at = now(), updated_at = now()
WHERE id = sqlc.arg(id) AND user_id = sqlc.arg(user_id) AND agent_id IS NOT DISTINCT FROM sqlc.narg(agent_id);

-- name: UpdateConversationArchived :exec
UPDATE ctx_conversation SET archived = sqlc.arg(archived), updated_at = now()
WHERE session_id = sqlc.arg(session_id) AND user_id = sqlc.arg(user_id) AND agent_id IS NOT DISTINCT FROM sqlc.narg(agent_id);

-- name: UpdateConversationLastActive :exec
UPDATE ctx_conversation SET last_active = now(), updated_at = now()
WHERE session_id = sqlc.arg(session_id) AND user_id = sqlc.arg(user_id) AND agent_id IS NOT DISTINCT FROM sqlc.narg(agent_id);

-- name: UpdateConversationTitleBySessionID :exec
UPDATE ctx_conversation SET title = sqlc.arg(title), updated_at = now()
WHERE session_id = sqlc.arg(session_id) AND user_id = sqlc.arg(user_id) AND agent_id IS NOT DISTINCT FROM sqlc.narg(agent_id);

-- name: UpdateConversationInfoBySessionID :exec
UPDATE ctx_conversation
SET
  title = CASE
    WHEN sqlc.narg(title)::text IS NOT NULL AND (title IS NULL OR title != sqlc.narg(title)) THEN sqlc.narg(title)
    ELSE title
  END,
  archived = CASE WHEN archived != sqlc.arg(archived) THEN sqlc.arg(archived) ELSE archived END,
  kind = CASE WHEN sqlc.narg(kind)::text IS NOT NULL AND kind != sqlc.narg(kind) THEN sqlc.narg(kind) ELSE kind END,
  project_id = CASE
    WHEN sqlc.narg(project_id)::text IS NOT NULL AND (project_id IS NULL OR project_id != sqlc.narg(project_id)) THEN sqlc.narg(project_id)
    ELSE project_id
  END,
  -- Make a legacy canonical group row durable: adopt the supplied group_id only
  -- when the stored value is NULL. Never overwrite or clear an existing group_id.
  group_id = CASE
    WHEN group_id IS NULL AND sqlc.narg(group_id)::uuid IS NOT NULL THEN sqlc.narg(group_id)
    ELSE group_id
  END,
  last_active = now(),
  updated_at = now()
WHERE session_id = sqlc.arg(session_id)
  AND user_id = sqlc.arg(user_id)
  AND agent_id IS NOT DISTINCT FROM sqlc.narg(agent_id);

-- name: ListConversations :many
SELECT * FROM ctx_conversation
WHERE user_id = sqlc.arg(user_id)
  AND (sqlc.narg(agent_id)::text IS NULL OR agent_id = sqlc.narg(agent_id))
  AND archived = false
ORDER BY last_active DESC;

-- name: ListConversationsAll :many
SELECT * FROM ctx_conversation
WHERE user_id = sqlc.arg(user_id)
  AND (sqlc.narg(agent_id)::text IS NULL OR agent_id = sqlc.narg(agent_id))
ORDER BY last_active DESC;

-- name: ListConversationsFiltered :many
SELECT * FROM ctx_conversation
WHERE user_id = sqlc.arg(user_id)
  AND agent_id IS NOT DISTINCT FROM sqlc.narg(agent_id)
  AND (sqlc.arg(include_archived) != 0 OR archived = false)
  AND (sqlc.arg(exclude_internal)::boolean = false OR kind NOT IN ('task', 'delegate'))
  AND (sqlc.narg(kind)::text IS NULL OR kind = sqlc.narg(kind))
  AND (sqlc.arg(project_id_is_null) = 0 OR project_id IS NULL)
  AND (sqlc.narg(project_id)::text IS NULL OR project_id = sqlc.narg(project_id))
ORDER BY last_active DESC
LIMIT NULLIF(sqlc.arg('limit'), -1) OFFSET sqlc.arg('offset');

-- name: ListAgentConversationLastActive :many
SELECT agent_id, MAX(last_active) AS last_active
FROM ctx_conversation
WHERE user_id = sqlc.arg(user_id)
  AND agent_id IS NOT NULL
  AND archived = false
GROUP BY agent_id;

-- name: ListConversationsForReviewByAgent :many
-- Ownerless legacy rows (NULL/empty user_id) are excluded: review is user-scoped
-- and such rows were never review candidates.
SELECT * FROM ctx_conversation
WHERE agent_id = sqlc.arg(agent_id)
  AND archived = false
  AND user_id IS NOT NULL AND user_id <> ''
ORDER BY last_active DESC;

-- name: ListConversationsForReviewFiltered :many
-- Ownerless legacy rows (NULL/empty user_id) are excluded: review is user-scoped
-- and such rows were never review candidates.
SELECT * FROM ctx_conversation
WHERE agent_id = sqlc.arg(agent_id)
  AND archived = false
  AND user_id IS NOT NULL AND user_id <> ''
  AND (sqlc.narg(kind)::text IS NULL OR kind = sqlc.narg(kind))
  AND (sqlc.arg(project_id_is_null) = 0 OR project_id IS NULL)
  AND (sqlc.narg(project_id)::text IS NULL OR project_id = sqlc.narg(project_id))
ORDER BY last_active DESC
LIMIT NULLIF(sqlc.arg('limit'), -1) OFFSET sqlc.arg('offset');

-- name: ListConversationsByKind :many
SELECT * FROM ctx_conversation WHERE agent_id = $1 AND user_id = $2 AND kind = $3 AND archived = false ORDER BY last_active DESC;

-- name: GetMainConversationByProject :one
SELECT * FROM ctx_conversation
WHERE project_id = sqlc.arg(project_id)
  AND user_id = sqlc.arg(user_id)
  AND agent_id IS NOT DISTINCT FROM sqlc.narg(agent_id)
  AND kind = 'main' AND archived = false LIMIT 1;

-- name: UpdateConversationKindProject :exec
UPDATE ctx_conversation SET kind = sqlc.arg(kind), project_id = sqlc.arg(project_id), updated_at = now()
WHERE session_id = sqlc.arg(session_id) AND user_id = sqlc.arg(user_id) AND agent_id IS NOT DISTINCT FROM sqlc.narg(agent_id);

-- Transaction-scoped advisory lock serializing per-conversation context writes:
-- the message seq + context-item ordinal allocation in Append, and the
-- compaction writeback that rewrites context items. Those are GetMax->++->insert
-- under Read Committed, which PostgreSQL runs in parallel across writers (and
-- nodes); the in-process striped mutex only serializes within one process. Held
-- until the enclosing tx ends. The 'ctxconv:' prefix keeps unrelated entities out
-- of this conversation's slot in the shared 64-bit lock space.
-- name: LockConversationForWrite :exec
SELECT pg_advisory_xact_lock(hashtextextended('ctxconv:' || sqlc.arg(conversation_id)::text, 0));
