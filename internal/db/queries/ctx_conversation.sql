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

-- name: GetConversationForSessionAccess :one
-- Private PEP lookup: it obtains durable owner/executor facts before the
-- SessionManager receives the resulting tenant scope. No transport may call it.
SELECT * FROM ctx_conversation
WHERE session_id = $1;

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

-- name: ArchiveActiveConversationBySessionID :execrows
-- Compare-and-rotate half of a session rotation: the row is archived only while
-- it is still active and still matches the caller's expected binding, so a
-- rotation that lost a race reports zero rows instead of archiving the successor
-- another rotation just created. The UPDATE also holds the row lock for the rest
-- of the enclosing transaction, serializing concurrent rotations of one session.
UPDATE ctx_conversation SET archived = true, updated_at = now()
WHERE session_id = sqlc.arg(session_id)
  AND user_id = sqlc.arg(user_id)
  AND agent_id IS NOT DISTINCT FROM sqlc.narg(agent_id)
  AND kind = sqlc.arg(kind)
  AND project_id IS NOT DISTINCT FROM sqlc.narg(project_id)
  AND archived = false;

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
  -- Make a legacy row's durable channel binding stick: adopt the supplied channel
  -- only when the stored one is blank (the column defaults to ''). Chat-channel
  -- sessions are resolved by this binding, so a row written before the binding
  -- existed has to acquire it once. Never overwrite an existing channel.
  channel = CASE
    WHEN channel = '' AND sqlc.narg(channel)::text IS NOT NULL THEN sqlc.narg(channel)
    ELSE channel
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

-- name: UpdateConversationTurnMetaBySessionID :execrows
-- The turn path's only write to a conversation row, guarded on that row still
-- being active. A rotation (`/new`, or the session_control tool) can archive the
-- session after a turn resolved it — auto-compaction widens that window to
-- minutes — and UpdateConversationInfoBySessionID would replay the turn-start
-- snapshot's `archived = false` over it. A resurrected kind=chat row then wins
-- its binding's newest-match lookup and drags the chat back into a conversation
-- the user already left. So this statement never mentions `archived`, `kind`, or
-- `project_id`, and `archived = false` in the predicate makes the check and the
-- write one atomic step: zero rows means the chat has moved on.
--
-- Title, channel, and group_id are adopted only into a blank stored value, so a
-- rename or a rebind that landed mid-turn is never clobbered by the turn's stale
-- snapshot either.
UPDATE ctx_conversation
SET
  title = CASE
    WHEN (title IS NULL OR title = '') AND sqlc.narg(title)::text IS NOT NULL THEN sqlc.narg(title)
    ELSE title
  END,
  channel = CASE
    WHEN channel = '' AND sqlc.narg(channel)::text IS NOT NULL THEN sqlc.narg(channel)
    ELSE channel
  END,
  group_id = CASE
    WHEN group_id IS NULL AND sqlc.narg(group_id)::uuid IS NOT NULL THEN sqlc.narg(group_id)
    ELSE group_id
  END,
  last_active = now(),
  updated_at = now()
WHERE session_id = sqlc.arg(session_id)
  AND user_id = sqlc.arg(user_id)
  AND agent_id IS NOT DISTINCT FROM sqlc.narg(agent_id)
  AND archived = false;

-- name: ListConversations :many
SELECT * FROM ctx_conversation
WHERE user_id = sqlc.arg(user_id)
  AND (sqlc.narg(agent_id)::text IS NULL OR agent_id = sqlc.narg(agent_id))
  AND archived = false
ORDER BY last_active DESC, session_id DESC;

-- name: ListConversationsAll :many
SELECT * FROM ctx_conversation
WHERE user_id = sqlc.arg(user_id)
  AND (sqlc.narg(agent_id)::text IS NULL OR agent_id = sqlc.narg(agent_id))
ORDER BY last_active DESC, session_id DESC;

-- name: ListConversationsFiltered :many
SELECT * FROM ctx_conversation
WHERE user_id = sqlc.arg(user_id)
  AND agent_id IS NOT DISTINCT FROM sqlc.narg(agent_id)
  AND (sqlc.arg(include_archived) != 0 OR archived = false)
  AND (sqlc.arg(exclude_internal)::boolean = false OR kind NOT IN ('task', 'delegate'))
  AND (sqlc.narg(kind)::text IS NULL OR kind = sqlc.narg(kind))
  -- Durable channel binding: chat-channel sessions are resolved by their channel
  -- rather than by a key-derived session id, so a channel can rotate onto a fresh
  -- session while its binding stays stable.
  AND (sqlc.narg(channel)::text IS NULL OR channel = sqlc.narg(channel))
  AND (sqlc.arg(project_id_is_null) = 0 OR project_id IS NULL)
  AND (sqlc.narg(project_id)::text IS NULL OR project_id = sqlc.narg(project_id))
ORDER BY last_active DESC, session_id DESC
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
ORDER BY last_active DESC, session_id DESC;

-- name: ListConversationsForReviewFiltered :many
-- Ownerless legacy rows (NULL/empty user_id) are excluded: review is user-scoped
-- and such rows were never review candidates.
-- An archived session is still a candidate when include_archived is set: rotation
-- (/new) archives a session the moment the user starts a fresh one, and its last
-- messages would otherwise never be distilled. The caller drops archived rows
-- once their review watermarks reach latest_seq.
SELECT
  sqlc.embed(c),
  COALESCE((
    SELECT MAX(m.seq)
    FROM ctx_message m
    WHERE m.conversation_id = c.id
  ), 0)::bigint AS latest_seq
FROM ctx_conversation c
WHERE c.agent_id = sqlc.arg(agent_id)
  AND (sqlc.arg(include_archived) != 0 OR c.archived = false)
  AND c.user_id IS NOT NULL AND c.user_id <> ''
  AND (sqlc.narg(kind)::text IS NULL OR c.kind = sqlc.narg(kind))
  AND (sqlc.arg(project_id_is_null) = 0 OR c.project_id IS NULL)
  AND (sqlc.narg(project_id)::text IS NULL OR c.project_id = sqlc.narg(project_id))
ORDER BY c.last_active DESC, c.session_id DESC
LIMIT NULLIF(sqlc.arg('limit'), -1) OFFSET sqlc.arg('offset');

-- name: ListConversationsByKind :many
SELECT * FROM ctx_conversation WHERE agent_id = $1 AND user_id = $2 AND kind = $3 AND archived = false ORDER BY last_active DESC, session_id DESC;

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
