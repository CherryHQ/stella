-- name: GetChannelBindingByKey :one
SELECT *
FROM channel_binding
WHERE channel_id = sqlc.arg(channel_id)
  AND platform = sqlc.arg(platform)
  AND chat_key = sqlc.arg(chat_key)
  AND thread_key = sqlc.arg(thread_key);

-- name: GetChannelBinding :one
SELECT *
FROM channel_binding
WHERE id = sqlc.arg(id)::uuid;

-- name: EnsureChannelBinding :exec
-- Admission creates the lane without taking an existing binding lock. The
-- caller acquires deployment -> principal -> binding locks before mutating
-- quota or sequence state.
INSERT INTO channel_binding (
    id, channel_id, platform, chat_key, thread_key,
    principal_kind, principal_id, agent_id
)
VALUES (
    sqlc.arg(id)::uuid, sqlc.arg(channel_id), sqlc.arg(platform),
    sqlc.arg(chat_key), sqlc.arg(thread_key), sqlc.arg(principal_kind),
    sqlc.arg(principal_id), sqlc.arg(agent_id)
)
ON CONFLICT (channel_id, platform, chat_key, thread_key) DO NOTHING;

-- name: GetChannelBindingForUpdate :one
SELECT *
FROM channel_binding
WHERE id = sqlc.arg(id)
FOR UPDATE;

-- name: UpsertChannelBinding :one
INSERT INTO channel_binding (
    id, channel_id, platform, chat_key, thread_key,
    principal_kind, principal_id, agent_id, session_id
)
VALUES (
    sqlc.arg(id)::uuid,
    sqlc.arg(channel_id),
    sqlc.arg(platform),
    sqlc.arg(chat_key),
    sqlc.arg(thread_key),
    sqlc.arg(principal_kind),
    sqlc.arg(principal_id),
    sqlc.arg(agent_id),
    NULLIF(sqlc.arg(session_id), '')
)
ON CONFLICT (channel_id, platform, chat_key, thread_key) DO UPDATE
SET principal_kind = excluded.principal_kind,
    principal_id = excluded.principal_id,
    agent_id = excluded.agent_id,
    session_id = COALESCE(excluded.session_id, channel_binding.session_id),
    revision = CASE
        WHEN channel_binding.principal_kind IS DISTINCT FROM excluded.principal_kind
          OR channel_binding.principal_id IS DISTINCT FROM excluded.principal_id
          OR channel_binding.agent_id IS DISTINCT FROM excluded.agent_id
          OR channel_binding.session_id IS DISTINCT FROM COALESCE(excluded.session_id, channel_binding.session_id)
        THEN channel_binding.revision + 1
        ELSE channel_binding.revision
    END,
    state = 'active',
    updated_at = now()
RETURNING *;

-- name: AllocateChannelBindingSeq :one
UPDATE channel_binding
SET next_seq = next_seq + 1,
    updated_at = now()
WHERE id = sqlc.arg(id)::uuid
RETURNING next_seq - 1 AS seq;

-- name: SetChannelBindingSession :one
UPDATE channel_binding
SET session_id = NULLIF(sqlc.arg(session_id), ''),
    revision = revision + 1,
    state = 'active',
    updated_at = now()
WHERE id = sqlc.arg(id)::uuid
RETURNING *;

-- name: RetireChannelBinding :execrows
UPDATE channel_binding
SET state = 'retired',
    revision = revision + 1,
    updated_at = now()
WHERE id = sqlc.arg(id)::uuid
  AND state <> 'retired';
