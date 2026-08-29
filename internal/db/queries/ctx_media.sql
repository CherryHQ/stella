-- name: CreateMediaIfAbsent :one
-- Exactly one of user_id / group_id carries the owner; the generated
-- (owner_kind, owner_id) pair is what the unique index and every read match on.
-- A conflict returns no rows. Callers must issue GetMediaByOwnerAndSHA256 in a
-- separate statement, then verify immutable metadata before reusing the row.
INSERT INTO ctx_media (user_id, group_id, sha256, mime_type, size_bytes)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (owner_kind, owner_id, sha256) DO NOTHING
RETURNING *;

-- name: GetMediaByOwnerAndSHA256 :one
SELECT * FROM ctx_media
WHERE owner_kind = sqlc.arg('owner_kind') AND owner_id = sqlc.arg('owner_id') AND sha256 = sqlc.arg('sha256');

-- name: ListMediaByIDsForOwner :many
-- Canonical append validates every media reference against this owner inside
-- its parent/parts transaction. Callers deduplicate IDs before this batch
-- lookup.
SELECT * FROM ctx_media
WHERE owner_kind = sqlc.arg('owner_kind')
  AND owner_id = sqlc.arg('owner_id')
  AND id = ANY(sqlc.arg('media_ids')::uuid[])
ORDER BY id ASC;

-- name: GetMediaForSession :one
-- Authorize immutable media through an ordinary session message part. The
-- conversation's legacy text owner is intentionally compared to the UUID media
-- owner, which for a group conversation is the group itself, so a session can
-- only ever resolve media its own principal owns. The caller passes the owner
-- kind its session identity implies (group when session.GroupID is set).
SELECT m.*
FROM ctx_media m
WHERE m.id = sqlc.arg(media_id)
  AND m.owner_kind = sqlc.arg(owner_kind)
  AND m.owner_id = sqlc.arg(owner_id)
  AND EXISTS (
      SELECT 1
      FROM ctx_message_part p
      JOIN ctx_message msg ON msg.id = p.message_id
      JOIN ctx_conversation c ON c.id = msg.conversation_id
      WHERE p.media_id = m.id
        AND c.session_id = sqlc.arg(session_id)
        AND c.user_id = m.owner_id::text
        AND c.agent_id IS NOT DISTINCT FROM sqlc.narg(agent_id)
  );

-- name: SetMediaBaselineIfAbsent :execrows
-- First write wins: the baseline of one media object is rendered once and then
-- immutable. Zero affected rows means another reader described the same bytes
-- first (or the row is not this owner's); the caller re-reads and adopts what is
-- already stored rather than overwriting it with an equally valid description.
UPDATE ctx_media
SET baseline = sqlc.arg('baseline'), updated_at = now()
WHERE id = sqlc.arg('id')
  AND owner_kind = sqlc.arg('owner_kind')
  AND owner_id = sqlc.arg('owner_id')
  AND baseline IS NULL;
